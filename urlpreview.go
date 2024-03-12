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
	"bytes"
	"context"
	"image"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/net/idna"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
)

func (portal *Portal) convertURLPreviewToBeeper(ctx context.Context, intent *appservice.IntentAPI, source *User, msg *waProto.ExtendedTextMessage) []*event.BeeperLinkPreview {
	if msg.GetMatchedText() == "" {
		return []*event.BeeperLinkPreview{}
	}

	output := &event.BeeperLinkPreview{
		MatchedURL: msg.GetMatchedText(),
		LinkPreview: event.LinkPreview{
			CanonicalURL: msg.GetCanonicalUrl(),
			Title:        msg.GetTitle(),
			Description:  msg.GetDescription(),
		},
	}
	if len(output.CanonicalURL) == 0 {
		output.CanonicalURL = output.MatchedURL
	}

	var thumbnailData []byte
	if msg.ThumbnailDirectPath != nil {
		var err error
		thumbnailData, err = source.Client.DownloadThumbnail(msg)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to download thumbnail for link preview")
		}
	}
	if thumbnailData == nil && msg.JpegThumbnail != nil {
		thumbnailData = msg.JpegThumbnail
	}
	if thumbnailData != nil {
		output.ImageHeight = int(msg.GetThumbnailHeight())
		output.ImageWidth = int(msg.GetThumbnailWidth())
		if output.ImageHeight == 0 || output.ImageWidth == 0 {
			src, _, err := image.Decode(bytes.NewReader(thumbnailData))
			if err == nil {
				imageBounds := src.Bounds()
				output.ImageWidth, output.ImageHeight = imageBounds.Max.X, imageBounds.Max.Y
			}
		}
		output.ImageSize = len(thumbnailData)
		output.ImageType = http.DetectContentType(thumbnailData)
		uploadData, uploadMime := thumbnailData, output.ImageType
		if portal.Encrypted {
			crypto := attachment.NewEncryptedFile()
			crypto.EncryptInPlace(uploadData)
			uploadMime = "application/octet-stream"
			output.ImageEncryption = &event.EncryptedFileInfo{EncryptedFile: *crypto}
		}
		resp, err := intent.UploadBytes(ctx, uploadData, uploadMime)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload thumbnail for link preview")
		} else {
			if output.ImageEncryption != nil {
				output.ImageEncryption.URL = resp.ContentURI.CUString()
			} else {
				output.ImageURL = resp.ContentURI.CUString()
			}
		}
	}
	if msg.GetPreviewType() == waProto.ExtendedTextMessage_VIDEO {
		output.Type = "video.other"
	}

	return []*event.BeeperLinkPreview{output}
}

var URLRegex = regexp.MustCompile(`https?://[^\s/_*]+(?:/\S*)?`)

func (portal *Portal) convertURLPreviewToWhatsApp(ctx context.Context, sender *User, content *event.MessageEventContent, dest *waProto.ExtendedTextMessage) bool {
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
	} else if portal.bridge.Config.Bridge.URLPreviews {
		if matchedURL := URLRegex.FindString(content.Body); len(matchedURL) == 0 {
			return false
		} else if parsed, err := url.Parse(matchedURL); err != nil {
			return false
		} else if parsed.Host, err = idna.ToASCII(parsed.Host); err != nil {
			return false
		} else if mxPreview, err := portal.MainIntent().GetURLPreview(ctx, parsed.String()); err != nil {
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
	if len(preview.CanonicalURL) > 0 {
		dest.CanonicalUrl = &preview.CanonicalURL
	}
	if len(preview.Description) > 0 {
		dest.Description = &preview.Description
	}
	if len(preview.Title) > 0 {
		dest.Title = &preview.Title
	}
	if strings.HasPrefix(preview.Type, "video.") {
		dest.PreviewType = waProto.ExtendedTextMessage_VIDEO.Enum()
	}
	imageMXC := preview.ImageURL.ParseOrIgnore()
	if preview.ImageEncryption != nil {
		imageMXC = preview.ImageEncryption.URL.ParseOrIgnore()
	}
	if !imageMXC.IsEmpty() {
		data, err := portal.MainIntent().DownloadBytes(ctx, imageMXC)
		if err != nil {
			log.Err(err).Str("image_url", string(preview.ImageURL)).Msg("Failed to download URL preview image")
			return true
		}
		if preview.ImageEncryption != nil {
			err = preview.ImageEncryption.DecryptInPlace(data)
			if err != nil {
				log.Err(err).Msg("Failed to decrypt URL preview image")
				return true
			}
		}
		dest.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
		uploadResp, err := sender.Client.Upload(ctx, data, whatsmeow.MediaLinkThumbnail)
		if err != nil {
			log.Err(err).Msg("Failed to reupload URL preview thumbnail")
			return true
		}
		dest.ThumbnailSha256 = uploadResp.FileSHA256
		dest.ThumbnailEncSha256 = uploadResp.FileEncSHA256
		dest.ThumbnailDirectPath = &uploadResp.DirectPath
		dest.MediaKey = uploadResp.MediaKey
		var width, height int
		dest.JpegThumbnail, width, height, err = createThumbnailAndGetSize(data, false)
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
