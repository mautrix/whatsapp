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

package connector

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func (wa *WhatsAppClient) obfuscateJID(jid types.JID) string {
	if jid.Server == types.HiddenUserServer {
		return jid.String()
	}
	// Turn the first 4 bytes of HMAC-SHA256(user_mxid, phone) into a number and replace the middle of the actual phone with that deterministic random number.
	randomNumber := binary.BigEndian.Uint32(hmac.New(sha256.New, []byte(wa.UserLogin.UserMXID)).Sum([]byte(jid.User))[:4])
	return fmt.Sprintf("+%s-%d-%s:%d", jid.User[:1], randomNumber, jid.User[len(jid.User)-2:], jid.Device)
}

func (wa *WhatsAppClient) trackUndecryptable(evt *events.UndecryptableMessage) {
	metricType := "error"
	if evt.IsUnavailable {
		metricType = "unavailable"
	}
	wa.UserLogin.TrackAnalytics("WhatsApp undecryptable message", map[string]any{
		"messageID":         evt.Info.ID,
		"undecryptableType": metricType,
		"decryptFailMode":   evt.DecryptFailMode,
	})
}

func (wa *WhatsAppClient) trackUndecryptableResolved(evt *events.Message) {
	resolveType := "sender"
	if evt.UnavailableRequestID != "" {
		resolveType = "phone"
	}
	wa.UserLogin.TrackAnalytics("WhatsApp undecryptable message resolved", map[string]any{
		"messageID":   evt.Info.ID,
		"resolveType": resolveType,
	})
}

func (wa *WhatsAppClient) trackFoundRetry(receipt *events.Receipt, messageID types.MessageID, retryCount int, msg *waE2E.Message) bool {
	wa.UserLogin.TrackAnalytics("WhatsApp incoming retry (accepted)", map[string]any{
		"requester":  wa.obfuscateJID(receipt.Sender),
		"messageID":  messageID,
		"retryCount": retryCount,
	})
	return true
}

func (wa *WhatsAppClient) trackNotFoundRetry(requester, to types.JID, id types.MessageID) *waE2E.Message {
	wa.UserLogin.TrackAnalytics("WhatsApp incoming retry (message not found)", map[string]any{
		"requester": wa.obfuscateJID(requester),
		"messageID": id,
	})
	return nil
}
