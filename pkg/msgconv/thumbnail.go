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
	"image/jpeg"
	"image/png"

	"github.com/rs/zerolog"
	"golang.org/x/image/draw"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const thumbnailMaxSize = 72
const thumbnailMinSize = 24

func createThumbnailAndGetSize(source []byte, pngThumbnail bool) ([]byte, int, int, error) {
	src, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to decode thumbnail: %w", err)
	}
	imageBounds := src.Bounds()
	width, height := imageBounds.Max.X, imageBounds.Max.Y
	var img image.Image
	if width <= thumbnailMaxSize && height <= thumbnailMaxSize {
		// No need to resize
		img = src
	} else {
		if width == height {
			width = thumbnailMaxSize
			height = thumbnailMaxSize
		} else if width < height {
			width /= height / thumbnailMaxSize
			height = thumbnailMaxSize
		} else {
			height /= width / thumbnailMaxSize
			width = thumbnailMaxSize
		}
		if width < thumbnailMinSize {
			width = thumbnailMinSize
		}
		if height < thumbnailMinSize {
			height = thumbnailMinSize
		}
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.NearestNeighbor.Scale(dst, dst.Rect, src, src.Bounds(), draw.Over, nil)
		img = dst
	}

	var buf bytes.Buffer
	if pngThumbnail {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpeg.DefaultQuality})
	}
	if err != nil {
		return nil, width, height, fmt.Errorf("failed to re-encode thumbnail: %w", err)
	}
	return buf.Bytes(), width, height, nil
}

func createThumbnail(source []byte, png bool) ([]byte, error) {
	data, _, _, err := createThumbnailAndGetSize(source, png)
	return data, err
}

func (mc *MessageConverter) downloadThumbnail(ctx context.Context, original []byte, thumbnailURL id.ContentURIString, thumbnailFile *event.EncryptedFileInfo, png bool) ([]byte, error) {
	if len(thumbnailURL) == 0 && thumbnailFile == nil {
		// just fall back to making thumbnail of original
	} else if thumbnail, err := mc.Bridge.Bot.DownloadMedia(ctx, thumbnailURL, thumbnailFile); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to download thumbnail in event, falling back to generating thumbnail from source")
	} else {
		return createThumbnail(thumbnail, png)
	}
	return createThumbnail(original, png)
}
