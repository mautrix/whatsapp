// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"encoding/json"
	"image"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/net/idna"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
)

type BeeperLinkPreview struct {
	mautrix.RespPreviewURL
	MatchedURL      string                   `json:"matched_url"`
	ImageEncryption *event.EncryptedFileInfo `json:"beeper:image:encryption,omitempty"`
}

func (portal *Portal) convertURLPreviewToBeeper(intent *appservice.IntentAPI, source *User, msg *waProto.ExtendedTextMessage) []*BeeperLinkPreview {
	if msg.GetMatchedText() == "" {
		return []*BeeperLinkPreview{}
	}

	output := &BeeperLinkPreview{
		MatchedURL: msg.GetMatchedText(),
		RespPreviewURL: mautrix.RespPreviewURL{
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
			portal.log.Warnfln("Failed to download thumbnail for link preview: %v", err)
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
		resp, err := intent.UploadBytes(uploadData, uploadMime)
		if err != nil {
			portal.log.Warnfln("Failed to reupload thumbnail for link preview: %v", err)
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

	return []*BeeperLinkPreview{output}
}

var URLRegex = regexp.MustCompile(`https?://[^\s/_*]+(?:/\S*)?`)

func (portal *Portal) convertURLPreviewToWhatsApp(ctx context.Context, sender *User, evt *event.Event, dest *waProto.ExtendedTextMessage) bool {
	var preview *BeeperLinkPreview

	rawPreview := gjson.GetBytes(evt.Content.VeryRaw, `com\.beeper\.linkpreviews`)
	if rawPreview.Exists() && rawPreview.IsArray() {
		var previews []BeeperLinkPreview
		if err := json.Unmarshal([]byte(rawPreview.Raw), &previews); err != nil || len(previews) == 0 {
			return false
		}
		// WhatsApp only supports a single preview.
		preview = &previews[0]
	} else if portal.bridge.Config.Bridge.URLPreviews {
		if matchedURL := URLRegex.FindString(evt.Content.AsMessage().Body); len(matchedURL) == 0 {
			return false
		} else if parsed, err := url.Parse(matchedURL); err != nil {
			return false
		} else if parsed.Host, err = idna.ToASCII(parsed.Host); err != nil {
			return false
		} else if mxPreview, err := portal.MainIntent().GetURLPreview(parsed.String()); err != nil {
			portal.log.Warnfln("Failed to fetch preview for %s: %v", matchedURL, err)
			return false
		} else {
			preview = &BeeperLinkPreview{
				RespPreviewURL: *mxPreview,
				MatchedURL:     matchedURL,
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
		data, err := portal.MainIntent().DownloadBytesContext(ctx, imageMXC)
		if err != nil {
			portal.log.Errorfln("Failed to download URL preview image %s in %s: %v", preview.ImageURL, evt.ID, err)
			return true
		}
		if preview.ImageEncryption != nil {
			err = preview.ImageEncryption.DecryptInPlace(data)
			if err != nil {
				portal.log.Errorfln("Failed to decrypt URL preview image in %s: %v", evt.ID, err)
				return true
			}
		}
		dest.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
		uploadResp, err := sender.Client.Upload(ctx, data, whatsmeow.MediaLinkThumbnail)
		if err != nil {
			portal.log.Errorfln("Failed to upload URL preview thumbnail in %s: %v", evt.ID, err)
			return true
		}
		dest.ThumbnailSha256 = uploadResp.FileSHA256
		dest.ThumbnailEncSha256 = uploadResp.FileEncSHA256
		dest.ThumbnailDirectPath = &uploadResp.DirectPath
		dest.MediaKey = uploadResp.MediaKey
		var width, height int
		dest.JpegThumbnail, width, height, err = createJPEGThumbnailAndGetSize(data)
		if err != nil {
			portal.log.Warnfln("Failed to create JPEG thumbnail for URL preview in %s: %v", evt.ID, err)
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
