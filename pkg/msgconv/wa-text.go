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

package msgconv

import (
	"context"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func (mc *MessageConverter) convertTextMessage(ctx context.Context, msg *waE2E.Message) (part *bridgev2.ConvertedMessagePart, contextInfo *waE2E.ContextInfo) {
	part = &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    msg.GetConversation(),
		},
	}
	if len(msg.GetExtendedTextMessage().GetText()) > 0 {
		part.Content.Body = msg.GetExtendedTextMessage().GetText()
	}
	contextInfo = msg.GetExtendedTextMessage().GetContextInfo()
	mc.parseFormatting(ctx, part.Content, false, false)
	part.Content.BeeperLinkPreviews = mc.convertURLPreviewToBeeper(ctx, msg.GetExtendedTextMessage())
	return
}

func (mc *MessageConverter) parseFormatting(ctx context.Context, content *event.MessageEventContent, allowInlineURL, forceHTML bool) {

}

func (mc *MessageConverter) parseFormattingToHTML(ctx context.Context, text string, allowInlineURL bool) string {
	return event.TextToHTML(text)
}
