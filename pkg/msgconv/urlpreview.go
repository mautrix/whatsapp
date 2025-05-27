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
	"image"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"golang.org/x/net/idna"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func (mc *MessageConverter) convertURLPreviewToBeeper(ctx context.Context, msg *waE2E.ExtendedTextMessage) []*event.BeeperLinkPreview {
	if msg.GetMatchedText() == "" {
		return []*event.BeeperLinkPreview{}
	}

	output := &event.BeeperLinkPreview{
		MatchedURL: msg.GetMatchedText(),
		LinkPreview: event.LinkPreview{
			Title:       msg.GetTitle(),
			Description: msg.GetDescription(),
		},
	}

	var thumbnailData []byte
	if msg.ThumbnailDirectPath != nil {
		var err error
		thumbnailData, err = getClient(ctx).DownloadThumbnail(ctx, msg)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to download thumbnail for link preview")
		}
	}
	if thumbnailData == nil && msg.JPEGThumbnail != nil {
		thumbnailData = msg.JPEGThumbnail
	}
	if thumbnailData != nil {
		output.ImageHeight = event.IntOrString(msg.GetThumbnailHeight())
		output.ImageWidth = event.IntOrString(msg.GetThumbnailWidth())
		if output.ImageHeight == 0 || output.ImageWidth == 0 {
			src, _, err := image.Decode(bytes.NewReader(thumbnailData))
			if err == nil {
				imageBounds := src.Bounds()
				output.ImageWidth, output.ImageHeight = event.IntOrString(imageBounds.Max.X), event.IntOrString(imageBounds.Max.Y)
			}
		}
		output.ImageSize = event.IntOrString(len(thumbnailData))
		output.ImageType = http.DetectContentType(thumbnailData)
		var err error
		output.ImageURL, output.ImageEncryption, err = getIntent(ctx).UploadMedia(
			ctx, getPortal(ctx).MXID, thumbnailData, "", output.ImageType,
		)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload thumbnail for link preview")
		}
	}
	if msg.GetPreviewType() == waE2E.ExtendedTextMessage_VIDEO {
		output.Type = "video.other"
	}

	return []*event.BeeperLinkPreview{output}
}

var URLRegex = regexp.MustCompile(`https?://[^\s/_*]+(?:/\S*)?`)

func (mc *MessageConverter) convertURLPreviewToWhatsApp(ctx context.Context, content *event.MessageEventContent, dest *waE2E.ExtendedTextMessage) bool {
	log := zerolog.Ctx(ctx)
	var preview *event.BeeperLinkPreview

	if content.BeeperLinkPreviews != nil {
		// Note: this check explicitly happens after checking for nil: empty arrays are treated as no previews,
		//       but omitting the field means the bridge may look for URLs in the message text.
		if len(content.BeeperLinkPreviews) == 0 {
			return false
		}
		// WhatsApp only supports a single preview.
		preview = content.BeeperLinkPreviews[0]
	} else if mc.FetchURLPreviews {
		if conn, ok := mc.Bridge.Matrix.(bridgev2.MatrixConnectorWithURLPreviews); !ok {
			return false
		} else if matchedURL := URLRegex.FindString(content.Body); len(matchedURL) == 0 {
			return false
		} else if parsed, err := url.Parse(matchedURL); err != nil {
			return false
		} else if parsed.Host, err = idna.ToASCII(parsed.Host); err != nil {
			return false
		} else if mxPreview, err := conn.GetURLPreview(ctx, parsed.String()); err != nil {
			log.Err(err).Str("url", matchedURL).Msg("Failed to fetch URL preview")
			return false
		} else {
			preview = &event.BeeperLinkPreview{
				LinkPreview: *mxPreview,
				MatchedURL:  matchedURL,
			}
		}
	}
	if preview == nil || len(preview.MatchedURL) == 0 {
		return false
	}

	dest.MatchedText = &preview.MatchedURL
	if len(preview.Description) > 0 {
		dest.Description = &preview.Description
	}
	if len(preview.Title) > 0 {
		dest.Title = &preview.Title
	}
	if strings.HasPrefix(preview.Type, "video.") {
		dest.PreviewType = waE2E.ExtendedTextMessage_VIDEO.Enum()
	}
	if preview.ImageURL != "" || preview.ImageEncryption != nil {
		data, err := mc.Bridge.Bot.DownloadMedia(ctx, preview.ImageURL, preview.ImageEncryption)
		if err != nil {
			log.Err(err).Str("image_url", string(preview.ImageURL)).Msg("Failed to download URL preview image")
			return true
		}
		dest.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
		uploadResp, err := getClient(ctx).Upload(ctx, data, whatsmeow.MediaLinkThumbnail)
		if err != nil {
			log.Err(err).Msg("Failed to reupload URL preview thumbnail")
			return true
		}
		dest.ThumbnailSHA256 = uploadResp.FileSHA256
		dest.ThumbnailEncSHA256 = uploadResp.FileEncSHA256
		dest.ThumbnailDirectPath = &uploadResp.DirectPath
		dest.MediaKey = uploadResp.MediaKey
		var width, height int
		dest.JPEGThumbnail, width, height, err = createThumbnailAndGetSize(data, false)
		if err != nil {
			log.Err(err).Msg("Failed to create JPEG thumbnail for URL preview")
		}
		if preview.ImageHeight > 0 && preview.ImageWidth > 0 {
			dest.ThumbnailWidth = proto.Uint32(uint32(preview.ImageWidth))
			dest.ThumbnailHeight = proto.Uint32(uint32(preview.ImageHeight))
		} else if width > 0 && height > 0 {
			dest.ThumbnailWidth = proto.Uint32(uint32(width))
			dest.ThumbnailHeight = proto.Uint32(uint32(height))
		}
	}
	return true
}
