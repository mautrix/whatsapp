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
	"fmt"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

func getMessageType(waMsg *waE2E.Message) string {
	switch {
	case waMsg == nil:
		return "ignore"
	case waMsg.Conversation != nil, waMsg.ExtendedTextMessage != nil:
		return "text"
	case waMsg.ImageMessage != nil:
		return fmt.Sprintf("image %s", waMsg.GetImageMessage().GetMimetype())
	case waMsg.StickerMessage != nil:
		return fmt.Sprintf("sticker %s", waMsg.GetStickerMessage().GetMimetype())
	case waMsg.VideoMessage != nil:
		return fmt.Sprintf("video %s", waMsg.GetVideoMessage().GetMimetype())
	case waMsg.PtvMessage != nil:
		return fmt.Sprintf("round video %s", waMsg.GetPtvMessage().GetMimetype())
	case waMsg.AudioMessage != nil:
		return fmt.Sprintf("audio %s", waMsg.GetAudioMessage().GetMimetype())
	case waMsg.DocumentMessage != nil:
		return fmt.Sprintf("document %s", waMsg.GetDocumentMessage().GetMimetype())
	case waMsg.ContactMessage != nil:
		return "contact"
	case waMsg.ContactsArrayMessage != nil:
		return "contact array"
	case waMsg.LocationMessage != nil:
		return "location"
	case waMsg.LiveLocationMessage != nil:
		return "live location start"
	case waMsg.GroupInviteMessage != nil:
		return "group invite"
	case waMsg.GroupMentionedMessage != nil:
		return "group mention"
	case waMsg.ScheduledCallCreationMessage != nil:
		return "scheduled call create"
	case waMsg.ScheduledCallEditMessage != nil:
		return "scheduled call edit"
	case waMsg.ReactionMessage != nil:
		if waMsg.ReactionMessage.GetText() == "" {
			return "reaction remove"
		}
		return "reaction"
	case waMsg.EncReactionMessage != nil:
		return "encrypted reaction"
	case waMsg.EncCommentMessage != nil:
		return "encrypted comment"
	case waMsg.CommentMessage != nil:
		return "comment"
	case waMsg.PollCreationMessage != nil || waMsg.PollCreationMessageV2 != nil || waMsg.PollCreationMessageV3 != nil:
		return "poll create"
	case waMsg.PollCreationMessageV4 != nil || waMsg.PollCreationMessageV5 != nil:
		return "poll create (vNext)"
	case waMsg.PollUpdateMessage != nil:
		return "poll update"
	case waMsg.ProtocolMessage != nil:
		switch waMsg.GetProtocolMessage().GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			if waMsg.GetProtocolMessage().GetKey() == nil {
				return "ignore"
			}
			return "revoke"
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			return "edit"
		case waE2E.ProtocolMessage_EPHEMERAL_SETTING:
			return "disappearing timer change"
		case waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE,
			waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION,
			waE2E.ProtocolMessage_INITIAL_SECURITY_NOTIFICATION_SETTING_SYNC:
			return "ignore"
		default:
			return fmt.Sprintf("unknown_protocol_%d", waMsg.GetProtocolMessage().GetType())
		}
	case waMsg.ButtonsMessage != nil:
		return "buttons"
	case waMsg.ButtonsResponseMessage != nil:
		return "buttons response"
	case waMsg.TemplateMessage != nil:
		return "template"
	case waMsg.HighlyStructuredMessage != nil:
		return "highly structured template"
	case waMsg.TemplateButtonReplyMessage != nil:
		return "template button reply"
	case waMsg.InteractiveMessage != nil:
		return "interactive"
	case waMsg.InteractiveResponseMessage != nil:
		return "interactive response"
	case waMsg.ListMessage != nil:
		return "list"
	case waMsg.ProductMessage != nil:
		return "product"
	case waMsg.ListResponseMessage != nil:
		return "list response"
	case waMsg.OrderMessage != nil:
		return "order"
	case waMsg.InvoiceMessage != nil:
		return "invoice"
	case waMsg.BotInvokeMessage != nil:
		return "bot invoke"
	case waMsg.EventMessage != nil:
		return "event"
	case waMsg.EventCoverImage != nil:
		return "event cover image"
	case waMsg.EncEventResponseMessage != nil:
		return "ignore" // these are ignored for now as they're not meant to be shown as new messages
		//return "encrypted event response"
	case waMsg.CommentMessage != nil:
		return "comment"
	case waMsg.EncCommentMessage != nil:
		return "encrypted comment"
	case waMsg.NewsletterAdminInviteMessage != nil:
		return "newsletter admin invite"
	case waMsg.SecretEncryptedMessage != nil:
		return "secret encrypted"
	case waMsg.PollResultSnapshotMessage != nil:
		return "poll result snapshot"
	case waMsg.MessageHistoryBundle != nil:
		return "message history bundle"
	case waMsg.RequestPhoneNumberMessage != nil:
		return "request phone number"
	case waMsg.KeepInChatMessage != nil:
		return "keep in chat"
	case waMsg.StatusMentionMessage != nil:
		return "status mention"
	case waMsg.StickerPackMessage != nil:
		return "sticker pack"
	case waMsg.AlbumMessage != nil:
		return "album" // or maybe these should be ignored?
	case waMsg.SendPaymentMessage != nil, waMsg.RequestPaymentMessage != nil,
		waMsg.DeclinePaymentRequestMessage != nil, waMsg.CancelPaymentRequestMessage != nil,
		waMsg.PaymentInviteMessage != nil:
		return "payment"
	case waMsg.Call != nil:
		return "call"
	case waMsg.Chat != nil:
		return "chat"
	case waMsg.PlaceholderMessage != nil:
		return "placeholder"
	case waMsg.SenderKeyDistributionMessage != nil, waMsg.StickerSyncRmrMessage != nil:
		return "ignore"
	default:
		return "unknown"
	}
}
