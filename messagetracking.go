// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/whatsmeow"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	errUserNotConnected            = errors.New("you are not connected to WhatsApp")
	errDifferentUser               = errors.New("user is not the recipient of this private chat portal")
	errUserNotLoggedIn             = errors.New("user is not logged in and chat has no relay bot")
	errRelaybotNotLoggedIn         = errors.New("neither user nor relay bot of chat are logged in")
	errMNoticeDisabled             = errors.New("bridging m.notice messages is disabled")
	errUnexpectedParsedContentType = errors.New("unexpected parsed content type")
	errInvalidGeoURI               = errors.New("invalid `geo:` URI in message")
	errUnknownMsgType              = errors.New("unknown msgtype")
	errMediaDownloadFailed         = errors.New("failed to download media")
	errMediaDecryptFailed          = errors.New("failed to decrypt media")
	errMediaConvertFailed          = errors.New("failed to convert media")
	errMediaWhatsAppUploadFailed   = errors.New("failed to upload media to WhatsApp")
	errMediaUnsupportedType        = errors.New("unsupported media type")
	errTargetNotFound              = errors.New("target event not found")
	errReactionDatabaseNotFound    = errors.New("reaction database entry not found")
	errReactionTargetNotFound      = errors.New("reaction target message not found")
	errTargetIsFake                = errors.New("target is a fake event")
	errReactionSentBySomeoneElse   = errors.New("target reaction was sent by someone else")
	errDMSentByOtherUser           = errors.New("target message was sent by the other user in a DM")
	errPollMissingQuestion         = errors.New("poll message is missing question")
	errPollDuplicateOption         = errors.New("poll options must be unique")

	errGalleryRelay   = errors.New("can't send gallery through relay user")
	errGalleryCaption = errors.New("can't send gallery with caption")

	errEditUnknownTarget     = errors.New("unknown edit target message")
	errEditUnknownTargetType = errors.New("unsupported edited message type")
	errEditDifferentSender   = errors.New("can't edit message sent by another user")
	errEditTooOld            = errors.New("message is too old to be edited")

	errBroadcastReactionNotSupported = errors.New("reacting to status messages is not currently supported")
	errBroadcastSendDisabled         = errors.New("sending status messages is disabled")

	errMessageDisconnected      = &whatsmeow.DisconnectedError{Action: "message send"}
	errMessageRetryDisconnected = &whatsmeow.DisconnectedError{Action: "message send (retry)"}

	errMessageTakingLong     = errors.New("bridging the message is taking longer than usual")
	errTimeoutBeforeHandling = errors.New("message timed out before handling was started")
)

func errorToStatusReason(err error) (reason event.MessageStatusReason, status event.MessageStatus, isCertain, sendNotice bool, humanMessage string) {
	switch {
	case errors.Is(err, whatsmeow.ErrBroadcastListUnsupported),
		errors.Is(err, errUnexpectedParsedContentType),
		errors.Is(err, errUnknownMsgType),
		errors.Is(err, errInvalidGeoURI),
		errors.Is(err, whatsmeow.ErrUnknownServer),
		errors.Is(err, whatsmeow.ErrRecipientADJID),
		errors.Is(err, errBroadcastReactionNotSupported),
		errors.Is(err, errBroadcastSendDisabled):
		return event.MessageStatusUnsupported, event.MessageStatusFail, true, true, ""
	case errors.Is(err, errMNoticeDisabled):
		return event.MessageStatusUnsupported, event.MessageStatusFail, true, false, ""
	case errors.Is(err, errMediaUnsupportedType),
		errors.Is(err, errPollMissingQuestion),
		errors.Is(err, errPollDuplicateOption),
		errors.Is(err, errEditDifferentSender),
		errors.Is(err, errEditTooOld),
		errors.Is(err, errEditUnknownTarget),
		errors.Is(err, errEditUnknownTargetType):
		return event.MessageStatusUnsupported, event.MessageStatusFail, true, true, err.Error()
	case errors.Is(err, errTimeoutBeforeHandling):
		return event.MessageStatusTooOld, event.MessageStatusRetriable, true, true, "the message was too old when it reached the bridge, so it was not handled"
	case errors.Is(err, context.DeadlineExceeded):
		return event.MessageStatusTooOld, event.MessageStatusRetriable, false, true, "handling the message took too long and was cancelled"
	case errors.Is(err, errMessageTakingLong):
		return event.MessageStatusTooOld, event.MessageStatusPending, false, true, err.Error()
	case errors.Is(err, errTargetNotFound),
		errors.Is(err, errTargetIsFake),
		errors.Is(err, errReactionDatabaseNotFound),
		errors.Is(err, errReactionTargetNotFound),
		errors.Is(err, errReactionSentBySomeoneElse),
		errors.Is(err, errDMSentByOtherUser):
		return event.MessageStatusGenericError, event.MessageStatusFail, true, false, ""
	case errors.Is(err, whatsmeow.ErrNotConnected),
		errors.Is(err, errUserNotConnected):
		return event.MessageStatusGenericError, event.MessageStatusRetriable, true, true, ""
	case errors.Is(err, errUserNotLoggedIn),
		errors.Is(err, errDifferentUser),
		errors.Is(err, errRelaybotNotLoggedIn):
		return event.MessageStatusGenericError, event.MessageStatusRetriable, true, false, ""
	case errors.Is(err, errMessageDisconnected),
		errors.Is(err, errMessageRetryDisconnected):
		return event.MessageStatusGenericError, event.MessageStatusRetriable, false, true, ""
	default:
		return event.MessageStatusGenericError, event.MessageStatusRetriable, false, true, ""
	}
}

func (portal *Portal) sendErrorMessage(ctx context.Context, evt *event.Event, err error, confirmed bool, editID id.EventID) id.EventID {
	if !portal.bridge.Config.Bridge.MessageErrorNotices {
		return ""
	}
	certainty := "may not have been"
	if confirmed {
		certainty = "was not"
	}
	var msgType string
	switch evt.Type {
	case event.EventMessage:
		msgType = "message"
	case event.EventReaction:
		msgType = "reaction"
	case event.EventRedaction:
		msgType = "redaction"
	case TypeMSC3381PollResponse, TypeMSC3381V2PollResponse:
		msgType = "poll response"
	case TypeMSC3381PollStart:
		msgType = "poll start"
	default:
		msgType = "unknown event"
	}
	msg := fmt.Sprintf("\u26a0 Your %s %s bridged: %v", msgType, certainty, err)
	if errors.Is(err, errMessageTakingLong) {
		msg = fmt.Sprintf("\u26a0 Bridging your %s is taking longer than usual", msgType)
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    msg,
	}
	if editID != "" {
		content.SetEdit(editID)
	} else {
		content.SetReply(evt)
	}
	resp, err := portal.sendMainIntentMessage(ctx, content)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send bridging error message")
		return ""
	}
	return resp.EventID
}

func (portal *Portal) sendStatusEvent(ctx context.Context, evtID, lastRetry id.EventID, err error, deliveredTo *[]id.UserID) {
	if !portal.bridge.Config.Bridge.MessageStatusEvents {
		return
	}
	if lastRetry == evtID {
		lastRetry = ""
	}
	intent := portal.bridge.Bot
	if !portal.Encrypted {
		// Bridge bot isn't present in unencrypted DMs
		intent = portal.MainIntent()
	}
	content := event.BeeperMessageStatusEventContent{
		Network: portal.getBridgeInfoStateKey(),
		RelatesTo: event.RelatesTo{
			Type:    event.RelReference,
			EventID: evtID,
		},
		DeliveredToUsers: deliveredTo,
		LastRetry:        lastRetry,
	}
	if err == nil {
		content.Status = event.MessageStatusSuccess
	} else {
		content.Reason, content.Status, _, _, content.Message = errorToStatusReason(err)
		content.Error = err.Error()
	}
	_, err = intent.SendMessageEvent(ctx, portal.MXID, event.BeeperMessageStatus, &content)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send message status event")
	}
}

func (portal *Portal) sendDeliveryReceipt(ctx context.Context, eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.SendReceipt(ctx, portal.MXID, eventID, event.ReceiptTypeRead, nil)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to mark message as read by bot (Matrix-side delivery receipt)")
		}
	}
}

func (portal *Portal) sendMessageMetrics(ctx context.Context, evt *event.Event, err error, part string, ms *metricSender) {
	origEvtID := evt.ID
	if retryMeta := evt.Content.AsMessage().MessageSendRetry; retryMeta != nil {
		origEvtID = retryMeta.OriginalEventID
	}
	if err != nil {
		level := zerolog.ErrorLevel
		if part == "Ignoring" {
			level = zerolog.DebugLevel
		}
		zerolog.Ctx(ctx).WithLevel(level).Err(err).Msg(part + " Matrix event")
		reason, statusCode, isCertain, sendNotice, _ := errorToStatusReason(err)
		checkpointStatus := status.ReasonToCheckpointStatus(reason, statusCode)
		portal.bridge.SendMessageCheckpoint(evt, status.MsgStepRemote, err, checkpointStatus, ms.getRetryNum())
		if sendNotice {
			ms.setNoticeID(portal.sendErrorMessage(ctx, evt, err, isCertain, ms.getNoticeID()))
		}
		portal.sendStatusEvent(ctx, origEvtID, evt.ID, err, nil)
	} else {
		zerolog.Ctx(ctx).Debug().Msg("Successfully handled Matrix event")
		portal.sendDeliveryReceipt(ctx, evt.ID)
		portal.bridge.SendMessageSuccessCheckpoint(evt, status.MsgStepRemote, ms.getRetryNum())
		var deliveredTo *[]id.UserID
		if portal.IsPrivateChat() {
			deliveredTo = &[]id.UserID{}
		}
		portal.sendStatusEvent(ctx, origEvtID, evt.ID, nil, deliveredTo)
		if prevNotice := ms.popNoticeID(); prevNotice != "" {
			_, _ = portal.MainIntent().RedactEvent(ctx, portal.MXID, prevNotice, mautrix.ReqRedact{
				Reason: "error resolved",
			})
		}
	}
	if ms != nil {
		zerolog.Ctx(ctx).Debug().Object("timings", ms.timings).Msg("Matrix event timings")
	}
}

type messageTimings struct {
	initReceive  time.Duration
	decrypt      time.Duration
	implicitRR   time.Duration
	portalQueue  time.Duration
	totalReceive time.Duration

	preproc   time.Duration
	convert   time.Duration
	whatsmeow whatsmeow.MessageDebugTimings
	totalSend time.Duration
}

func (mt *messageTimings) MarshalZerologObject(e *zerolog.Event) {
	e.Dur("init_receive", mt.initReceive).
		Dur("decrypt", mt.decrypt).
		Dur("implicit_rr", mt.implicitRR).
		Dur("portal_queue", mt.portalQueue).
		Dur("total_receive", mt.totalReceive).
		Dur("preproc", mt.preproc).
		Dur("convert", mt.convert).
		Object("whatsmeow", mt.whatsmeow).
		Dur("total_send", mt.totalSend)
}

type metricSender struct {
	portal         *Portal
	previousNotice id.EventID
	lock           sync.Mutex
	completed      bool
	retryNum       int
	timings        *messageTimings
}

func (ms *metricSender) getRetryNum() int {
	if ms != nil {
		return ms.retryNum
	}
	return 0
}

func (ms *metricSender) getNoticeID() id.EventID {
	if ms == nil {
		return ""
	}
	return ms.previousNotice
}

func (ms *metricSender) popNoticeID() id.EventID {
	if ms == nil {
		return ""
	}
	evtID := ms.previousNotice
	ms.previousNotice = ""
	return evtID
}

func (ms *metricSender) setNoticeID(evtID id.EventID) {
	if ms != nil && ms.previousNotice == "" {
		ms.previousNotice = evtID
	}
}

func (ms *metricSender) sendMessageMetrics(ctx context.Context, evt *event.Event, err error, part string, completed bool) {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if !completed && ms.completed {
		return
	}
	ms.portal.sendMessageMetrics(ctx, evt, err, part, ms)
	ms.retryNum++
	ms.completed = completed
}
