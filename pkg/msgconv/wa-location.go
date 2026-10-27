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
	"bytes"
	"context"
	"fmt"
	"html"
	"image"
	"math"
	"net/http"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func formatCoordinates(lat, long float64) string {
	latChar := 'N'
	if lat < 0 {
		latChar = 'S'
	}
	longChar := 'E'
	if long < 0 {
		longChar = 'W'
	}
	return fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(lat), latChar, math.Abs(long), longChar)
}

func addLocationThumbnail(ctx context.Context, content *event.MessageEventContent, thumbnail []byte) {
	if len(thumbnail) == 0 {
		return
	}
	thumbnailMime := http.DetectContentType(thumbnail)
	thumbnailURL, thumbnailFile, err := getIntent(ctx).UploadMedia(ctx, getPortal(ctx).MXID, thumbnail, "thumb.jpeg", thumbnailMime)
	if err != nil {
		return
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(thumbnail))
	content.Info = &event.FileInfo{
		ThumbnailInfo: &event.FileInfo{
			Size:     len(thumbnail),
			Width:    cfg.Width,
			Height:   cfg.Height,
			MimeType: thumbnailMime,
		},
		ThumbnailURL:  thumbnailURL,
		ThumbnailFile: thumbnailFile,
	}
}

func (mc *MessageConverter) convertLocationMessage(ctx context.Context, msg *waE2E.LocationMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	url := msg.GetURL()
	if len(url) == 0 {
		url = fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude())
	}
	name := msg.GetName()
	if len(name) == 0 {
		name = formatCoordinates(msg.GetDegreesLatitude(), msg.GetDegreesLongitude())
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgLocation,
		Body:          fmt.Sprintf("Location: %s\n%s\n%s", name, msg.GetAddress(), url),
		Format:        event.FormatHTML,
		FormattedBody: fmt.Sprintf(`Location: <a href="%s">%s</a><br>%s`, html.EscapeString(url), html.EscapeString(name), html.EscapeString(msg.GetAddress())),
		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude()),
	}
	addLocationThumbnail(ctx, content, msg.GetJPEGThumbnail())

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
	}, msg.GetContextInfo()
}

func (mc *MessageConverter) convertLiveLocationMessage(ctx context.Context, msg *waE2E.LiveLocationMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	if msg.DegreesLatitude == nil || msg.DegreesLongitude == nil {
		content := &event.MessageEventContent{
			Body:    "Started sharing live location",
			MsgType: event.MsgNotice,
		}
		if len(msg.GetCaption()) > 0 {
			content.Body += ": " + msg.GetCaption()
		}
		content.Body += "\n\nUse the WhatsApp app to see the location."
		return &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: content,
		}, msg.GetContextInfo()
	}

	lat, long := msg.GetDegreesLatitude(), msg.GetDegreesLongitude()
	url := fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", lat, long)
	name := formatCoordinates(lat, long)
	const note = "Live updates are only visible in the WhatsApp app."
	body := fmt.Sprintf("Live location: %s\n%s", name, url)
	formattedBody := fmt.Sprintf(`Live location: <a href="%s">%s</a>`, html.EscapeString(url), html.EscapeString(name))
	if caption := msg.GetCaption(); len(caption) > 0 {
		body += "\n" + caption
		formattedBody += "<br>" + html.EscapeString(caption)
	}
	content := &event.MessageEventContent{
		MsgType:       event.MsgLocation,
		Body:          body + "\n\n" + note,
		Format:        event.FormatHTML,
		FormattedBody: formattedBody + "<br><br>" + note,
		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", lat, long),
	}
	addLocationThumbnail(ctx, content, msg.GetJPEGThumbnail())

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
	}, msg.GetContextInfo()
}
