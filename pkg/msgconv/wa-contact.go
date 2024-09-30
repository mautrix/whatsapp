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
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func (mc *MessageConverter) convertContactMessage(ctx context.Context, msg *waE2E.ContactMessage) (part *bridgev2.ConvertedMessagePart, contextInfo *waE2E.ContextInfo) {
	fileName := fmt.Sprintf("%s.vcf", msg.GetDisplayName())
	data := []byte(msg.GetVcard())
	mimeType := "text/vcard"
	contextInfo = msg.GetContextInfo()

	mxc, file, err := getIntent(ctx).UploadMedia(ctx, getPortal(ctx).MXID, data, fileName, mimeType)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload WhatsApp contact message")
		part = &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Failed to reupload vcard",
			},
		}
		return
	}

	part = &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:     fileName,
			FileName: fileName,
			URL:      mxc,
			Info: &event.FileInfo{
				MimeType: mimeType,
				Size:     len(msg.GetVcard()),
			},
			File:    file,
			MsgType: event.MsgFile,
		},
		Extra: make(map[string]any),
	}

	return
}

func (mc *MessageConverter) convertContactsArrayMessage(ctx context.Context, msg *waE2E.ContactsArrayMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    "Contact array messages are not yet supported",
		},
	}, msg.GetContextInfo()
}
