// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 Gerardo Rodriguez
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
	"encoding/base64"
	"encoding/json"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

const MessageDataField = "fi.mau.whatsapp.message_data"
const maxQuoteDataSize = 16 * 1024

func (mc *MessageConverter) getQuotedMessage(ctx context.Context, replyTo *database.Message, relatesTo *event.RelatesTo, portal *bridgev2.Portal) *waE2E.Message {
	if relatesTo != nil && relatesTo.InReplyTo != nil && relatesTo.GetReplyTo() == replyTo.MXID && len(relatesTo.InReplyTo.BeeperQuote) > 0 {
		if quote := mc.convertQuote(ctx, relatesTo.InReplyTo.BeeperQuote); quote != nil {
			return quote
		}
	}
	log := zerolog.Ctx(ctx).With().Stringer("reply_to_event_id", replyTo.MXID).Logger()
	evt, err := mc.Bridge.Bot.GetEvent(ctx, portal.MXID, replyTo.MXID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to fetch quoted event")
	} else if evt == nil {
		log.Debug().Msg("Quoted event not found")
	} else if evt.RoomID != "" && evt.RoomID != portal.MXID {
		log.Warn().Msg("Quoted event is in a different room")
	} else if evt.Unsigned.RedactedBecause == nil {
		var data []byte
		data, err = json.Marshal(evt.Content)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to marshal quoted event content")
		} else if quote := mc.convertQuote(ctx, data); quote != nil {
			return quote
		}
	}
	return &waE2E.Message{Conversation: proto.String("")}
}

func (mc *MessageConverter) convertQuote(ctx context.Context, data json.RawMessage) *waE2E.Message {
	var content struct {
		event.MessageEventContent
		MessageData json.RawMessage  `json:"fi.mau.whatsapp.message_data"`
		PollStart   *event.PollStart `json:"org.matrix.msc3381.poll.start"`
	}
	if err := json.Unmarshal(data, &content); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to parse quoted event content")
		return nil
	}
	if len(content.MessageData) > 0 {
		var encoded string
		if err := json.Unmarshal(content.MessageData, &encoded); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to parse quoted WhatsApp message data")
		} else if quote := decodeMessageData(ctx, encoded); quote != nil {
			return quote
		}
	}
	content.Mentions = &event.Mentions{}
	if content.PollStart != nil {
		text, _ := mc.msc1767ToWhatsApp(ctx, content.PollStart.Question, content.Mentions)
		return &waE2E.Message{Conversation: proto.String(text)}
	}
	content.RemoveReplyFallback()
	text, _ := mc.parseText(ctx, &content.MessageEventContent)
	if text == "" {
		return nil
	}
	return &waE2E.Message{Conversation: proto.String(text)}
}

func decodeMessageData(ctx context.Context, encoded string) *waE2E.Message {
	if len(encoded) > maxQuoteDataSize {
		zerolog.Ctx(ctx).Warn().Int("size", len(encoded)).Msg("Quoted WhatsApp message data is too large")
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decode quoted WhatsApp message data")
		return nil
	}
	var msg waE2E.Message
	if err = proto.Unmarshal(data, &msg); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to unmarshal quoted WhatsApp message data")
		return nil
	}
	return makeQuote(&msg)
}

func (mc *MessageConverter) addMessageData(ctx context.Context, part *bridgev2.ConvertedMessagePart, msg *waE2E.Message, isViewOnce bool) {
	if isViewOnce {
		delete(part.Extra, MessageDataField)
		return
	}
	if msg.Conversation != nil || msg.ExtendedTextMessage != nil {
		return
	}
	quote := makeQuote(msg)
	if quote == nil {
		return
	}
	if encoded, ok := part.Extra[MessageDataField].(string); ok {
		if previous := decodeMessageData(ctx, encoded); previous != nil {
			switch {
			case quote.ImageMessage != nil && quote.ImageMessage.GetDirectPath() == "" && previous.ImageMessage != nil:
				previous.ImageMessage.Caption = quote.ImageMessage.Caption
				quote = previous
			case quote.VideoMessage != nil && quote.VideoMessage.GetDirectPath() == "" && previous.VideoMessage != nil:
				previous.VideoMessage.Caption = quote.VideoMessage.Caption
				quote = previous
			case quote.DocumentMessage != nil && quote.DocumentMessage.GetDirectPath() == "" && previous.DocumentMessage != nil:
				previous.DocumentMessage.Caption = quote.DocumentMessage.Caption
				quote = previous
			}
		}
	}
	delete(part.Extra, MessageDataField)
	if size := base64.StdEncoding.EncodedLen(proto.Size(quote)); size > maxQuoteDataSize {
		zerolog.Ctx(ctx).Debug().Int("size", size).Msg("Not storing oversized WhatsApp quote data")
		return
	}
	data, err := proto.Marshal(quote)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to marshal WhatsApp message data")
		return
	}
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	part.Extra[MessageDataField] = base64.StdEncoding.EncodeToString(data)
}

func makeQuote(msg *waE2E.Message) *waE2E.Message {
	switch {
	case msg == nil:
		return nil
	case msg.Conversation != nil:
		return &waE2E.Message{Conversation: msg.Conversation}
	case msg.ExtendedTextMessage != nil:
		return &waE2E.Message{Conversation: msg.ExtendedTextMessage.Text}
	case msg.ImageMessage != nil && !msg.ImageMessage.GetViewOnce():
		m := msg.ImageMessage
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL:               m.URL,
			DirectPath:        m.DirectPath,
			MediaKey:          m.MediaKey,
			FileSHA256:        m.FileSHA256,
			FileEncSHA256:     m.FileEncSHA256,
			FileLength:        m.FileLength,
			MediaKeyTimestamp: m.MediaKeyTimestamp,
			Mimetype:          m.Mimetype,
			Caption:           m.Caption,
			Height:            m.Height,
			Width:             m.Width,
			JPEGThumbnail:     m.JPEGThumbnail,
		}}
	case msg.VideoMessage != nil && !msg.VideoMessage.GetViewOnce():
		return &waE2E.Message{VideoMessage: quoteVideo(msg.VideoMessage)}
	case msg.PtvMessage != nil && !msg.PtvMessage.GetViewOnce():
		return &waE2E.Message{PtvMessage: quoteVideo(msg.PtvMessage)}
	case msg.AudioMessage != nil && !msg.AudioMessage.GetViewOnce():
		m := msg.AudioMessage
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL:               m.URL,
			DirectPath:        m.DirectPath,
			MediaKey:          m.MediaKey,
			FileSHA256:        m.FileSHA256,
			FileEncSHA256:     m.FileEncSHA256,
			FileLength:        m.FileLength,
			MediaKeyTimestamp: m.MediaKeyTimestamp,
			Mimetype:          m.Mimetype,
			Seconds:           m.Seconds,
			PTT:               m.PTT,
		}}
	case msg.DocumentMessage != nil:
		m := msg.DocumentMessage
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL:               m.URL,
			DirectPath:        m.DirectPath,
			MediaKey:          m.MediaKey,
			FileSHA256:        m.FileSHA256,
			FileEncSHA256:     m.FileEncSHA256,
			FileLength:        m.FileLength,
			MediaKeyTimestamp: m.MediaKeyTimestamp,
			Mimetype:          m.Mimetype,
			Title:             m.Title,
			FileName:          m.FileName,
			Caption:           m.Caption,
			PageCount:         m.PageCount,
			JPEGThumbnail:     m.JPEGThumbnail,
		}}
	case msg.StickerMessage != nil:
		m := msg.StickerMessage
		return &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			URL:               m.URL,
			DirectPath:        m.DirectPath,
			MediaKey:          m.MediaKey,
			FileSHA256:        m.FileSHA256,
			FileEncSHA256:     m.FileEncSHA256,
			FileLength:        m.FileLength,
			MediaKeyTimestamp: m.MediaKeyTimestamp,
			Mimetype:          m.Mimetype,
			Height:            m.Height,
			Width:             m.Width,
			IsAnimated:        m.IsAnimated,
			IsLottie:          m.IsLottie,
			PngThumbnail:      m.PngThumbnail,
		}}
	case msg.LocationMessage != nil:
		m := msg.LocationMessage
		return &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  m.DegreesLatitude,
			DegreesLongitude: m.DegreesLongitude,
			Name:             m.Name,
			Address:          m.Address,
			URL:              m.URL,
			Comment:          m.Comment,
			JPEGThumbnail:    m.JPEGThumbnail,
		}}
	case msg.ContactMessage != nil:
		m := msg.ContactMessage
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
			DisplayName: m.DisplayName,
			Vcard:       m.Vcard,
		}}
	case msg.ContactsArrayMessage != nil:
		m := msg.ContactsArrayMessage
		contacts := make([]*waE2E.ContactMessage, len(m.Contacts))
		for i, contact := range m.Contacts {
			contacts[i] = &waE2E.ContactMessage{
				DisplayName: proto.String(contact.GetDisplayName()),
				Vcard:       proto.String(contact.GetVcard()),
			}
		}
		return &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{
			DisplayName: m.DisplayName,
			Contacts:    contacts,
		}}
	default:
		return nil
	}
}

func quoteVideo(m *waE2E.VideoMessage) *waE2E.VideoMessage {
	return &waE2E.VideoMessage{
		URL:               m.URL,
		DirectPath:        m.DirectPath,
		MediaKey:          m.MediaKey,
		FileSHA256:        m.FileSHA256,
		FileEncSHA256:     m.FileEncSHA256,
		FileLength:        m.FileLength,
		MediaKeyTimestamp: m.MediaKeyTimestamp,
		Mimetype:          m.Mimetype,
		Caption:           m.Caption,
		Height:            m.Height,
		Width:             m.Width,
		JPEGThumbnail:     m.JPEGThumbnail,
		Seconds:           m.Seconds,
		GifPlayback:       m.GifPlayback,
	}
}
