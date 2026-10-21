// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.mau.fi/whatsmeow"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// wrapConversionError turns a failure to convert a Matrix event into a message status. Nothing has been
// sent to WhatsApp at that point, so the failure is certain.
func wrapConversionError(err error) error {
	if err == nil || errors.As(err, &bridgev2.MessageStatus{}) {
		return err
	}
	return bridgev2.WrapErrorInStatus(err).
		WithMessage("failed to convert message for WhatsApp").
		WithIsCertain(true).
		WithSendNotice(true)
}

// wrapWhatsAppError turns an error from a whatsmeow request made while handling a Matrix event into a
// message status.
func wrapWhatsAppError(err error) error {
	if err == nil || errors.As(err, &bridgev2.MessageStatus{}) {
		return err
	}
	var iqErr *whatsmeow.IQError
	var disconnectErr *whatsmeow.DisconnectedError
	switch {
	case errors.Is(err, whatsmeow.ErrNotConnected), errors.Is(err, whatsmeow.ErrClientIsNil):
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("not connected to WhatsApp").
			WithIsCertain(true).
			WithSendNotice(true)
	case errors.Is(err, whatsmeow.ErrMessageTimedOut), errors.Is(err, whatsmeow.ErrIQTimedOut),
		errors.Is(err, context.DeadlineExceeded), errors.As(err, &disconnectErr):
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("no response from WhatsApp").
			WithIsCertain(false).
			WithSendNotice(true)
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit):
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("rate limited by WhatsApp").
			WithErrorReason(event.MessageStatusNetworkError).
			WithIsCertain(true).
			WithSendNotice(true)
	case errors.Is(err, whatsmeow.ErrIQForbidden), errors.Is(err, whatsmeow.ErrIQNotAuthorized):
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("you don't have permission to do that on WhatsApp").
			WithErrorReason(event.MessageStatusNoPermission).
			WithStatus(event.MessageStatusFail).
			WithIsCertain(true).
			WithSendNotice(true)
	case errors.Is(err, whatsmeow.ErrServerReturnedError), errors.As(err, &iqErr):
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("WhatsApp returned an error").
			WithIsCertain(true).
			WithSendNotice(true)
	default:
		return bridgev2.WrapErrorInStatus(err).
			WithMessage("unexpected error from WhatsApp").
			WithSendNotice(true)
	}
}

// wrapWhatsAppRespError turns an error from a whatsmeow request made during login or provisioning into
// an HTTP error, keeping the original error in the chain. Clients retry 429 and 502-504 automatically,
// which isn't wanted for provisioning requests, so those statuses aren't used.
func wrapWhatsAppRespError(err error) error {
	var mapped bridgev2.RespError
	var disconnectErr *whatsmeow.DisconnectedError
	switch {
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit):
		mapped = bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("Rate limited by WhatsApp. Try again later.").WithStatus(http.StatusBadRequest))
	case errors.Is(err, whatsmeow.ErrNotConnected), errors.Is(err, whatsmeow.ErrClientIsNil), errors.As(err, &disconnectErr):
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Not connected to WhatsApp"))
	case errors.Is(err, whatsmeow.ErrIQTimedOut), errors.Is(err, context.DeadlineExceeded):
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("WhatsApp didn't respond in time"))
	case errors.Is(err, whatsmeow.ErrIQForbidden), errors.Is(err, whatsmeow.ErrIQNotAuthorized):
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("WhatsApp denied the request"))
	default:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Request to WhatsApp failed"))
	}
	return fmt.Errorf("%w: %w", mapped.WithInternalError(err), err)
}
