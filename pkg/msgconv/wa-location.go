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
	"image"
	"math"
	"net/http"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func (mc *MessageConverter) convertLocationMessage(ctx context.Context, msg *waE2E.LocationMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
	url := msg.GetURL()
	if len(url) == 0 {
		url = fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude())
	}
	name := msg.GetName()
	if len(name) == 0 {
		latChar := 'N'
		if msg.GetDegreesLatitude() < 0 {
			latChar = 'S'
		}
		longChar := 'E'
		if msg.GetDegreesLongitude() < 0 {
			longChar = 'W'
		}
		name = fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(msg.GetDegreesLatitude()), latChar, math.Abs(msg.GetDegreesLongitude()), longChar)
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgLocation,
		Body:          fmt.Sprintf("Location: %s\n%s\n%s", name, msg.GetAddress(), url),
		Format:        event.FormatHTML,
		FormattedBody: fmt.Sprintf("Location: <a href='%s'>%s</a><br>%s", url, name, msg.GetAddress()),
		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude()),
	}

	if len(msg.GetJPEGThumbnail()) > 0 {
		thumbnailMime := http.DetectContentType(msg.GetJPEGThumbnail())
		thumbnailURL, thumbnailFile, err := getIntent(ctx).UploadMedia(ctx, getPortal(ctx).MXID, msg.GetJPEGThumbnail(), "thumb.jpeg", thumbnailMime)
		if err == nil {
			cfg, _, _ := image.DecodeConfig(bytes.NewReader(msg.GetJPEGThumbnail()))
			content.Info = &event.FileInfo{
				ThumbnailInfo: &event.FileInfo{
					Size:     len(msg.GetJPEGThumbnail()),
					Width:    cfg.Width,
					Height:   cfg.Height,
					MimeType: thumbnailMime,
				},
				ThumbnailURL:  thumbnailURL,
				ThumbnailFile: thumbnailFile,
			}
		}
	}

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
	}, msg.GetContextInfo()
}

func (mc *MessageConverter) convertLiveLocationMessage(ctx context.Context, msg *waE2E.LiveLocationMessage) (*bridgev2.ConvertedMessagePart, *waE2E.ContextInfo) {
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
