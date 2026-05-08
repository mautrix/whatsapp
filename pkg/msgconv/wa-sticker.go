// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
	"go.mau.fi/util/exstrings"
	"go.mau.fi/util/lottie"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) GetCachedStickerPack(ctx context.Context, client *whatsmeow.Client, packID string) (*types.StickerPack, error) {
	mc.stickerPackCacheLock.Lock()
	defer mc.stickerPackCacheLock.Unlock()
	cached, ok := mc.stickerPackCache[packID]
	if ok {
		if cached == nil {
			return nil, bridgev2.RespError(mautrix.MNotFound.WithMessage("sticker pack not found (cached)"))
		}
		return cached, nil
	}

	pack, err := client.FetchStickerPack(ctx, packID)
	if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) {
		mc.stickerPackCache[packID] = nil
		return nil, bridgev2.WrapRespErr(err, mautrix.MNotFound)
	} else if err != nil {
		return nil, err
	}
	mc.stickerPackCache[packID] = pack
	if packID != pack.StickerPackID {
		mc.stickerPackCache[pack.StickerPackID] = pack
	}
	return pack, nil
}

func (mc *MessageConverter) GetCachedSticker(ctx context.Context, client *whatsmeow.Client, packID string, hash []byte) (*types.StickerPackItem, error) {
	pack, err := mc.GetCachedStickerPack(ctx, client, packID)
	if err != nil {
		return nil, err
	}
	for _, sticker := range pack.Stickers {
		if bytes.Equal(sticker.FileHash, hash) {
			return sticker, nil
		}
	}
	return nil, nil
}

func (mc *MessageConverter) DownloadImagePack(ctx context.Context, userLoginID networkid.UserLoginID, client *whatsmeow.Client, inputURL string) (*bridgev2.ImportedImagePack, error) {
	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return nil, bridgev2.WrapRespErr(err, mautrix.MNotFound)
	} else if parsedURL.Host != "api.whatsapp.com" && parsedURL.Host != "wa.me" {
		return nil, bridgev2.WrapRespErr(fmt.Errorf("invalid host %q", parsedURL.Host), mautrix.MNotFound)
	} else if !strings.HasPrefix(parsedURL.Path, "/stickerpack/") {
		return nil, bridgev2.WrapRespErr(fmt.Errorf("invalid path %q", parsedURL.Path), mautrix.MNotFound)
	}
	packName := strings.Split(strings.TrimPrefix(parsedURL.Path, "/stickerpack/"), "/")[0]
	if packName == "" {
		return nil, bridgev2.WrapRespErr(fmt.Errorf("empty pack name"), mautrix.MNotFound)
	}
	pack, err := mc.GetCachedStickerPack(ctx, client, packName)
	if err != nil {
		return nil, err
	}
	canonicalURL := "https://wa.me/stickerpack/" + pack.StickerPackID
	topLevelExtra := map[string]any{
		"fi.mau.whatsapp.stickerpack": map[string]any{
			"id":          pack.StickerPackID,
			"name":        pack.Name,
			"description": pack.Description,
			"publisher":   pack.Publisher,
			"animated":    pack.Animated > 0,
			"lottie":      pack.Lottie > 0,
		},
	}
	content := &event.ImagePackEventContent{
		Images: make(map[string]*event.ImagePackImage, len(pack.Stickers)),
		Metadata: event.ImagePackMetadata{
			DisplayName: pack.Name,
			AvatarURL:   "",
			Usage:       []event.ImagePackUsage{event.ImagePackUsageSticker},
			Attribution: fmt.Sprintf("By %s on WhatsApp %s", pack.Publisher, canonicalURL),
			BridgedPack: &event.BridgedStickerPack{
				Network: StickerSourceID,
				URL:     canonicalURL,
			},
		},
	}
	ctx = context.WithValue(ctx, contextKeyClient, client)
	ctx = context.WithValue(ctx, contextKeyIntent, mc.Bridge.Bot)
	ctx = context.WithValue(ctx, contextKeyPortal, (*bridgev2.Portal)(nil))
	for i, sticker := range pack.Stickers {
		shortcode := sticker.PreviewWebpID
		if shortcode == "" {
			shortcode = fmt.Sprintf("%s_img%d", pack.StickerPackID, i+1)
		}
		body := sticker.AccessibilityText
		var emoji string
		if len(sticker.Emojis) > 0 {
			emoji = sticker.Emojis[0]
			if body == "" {
				body = strings.Join(sticker.Emojis, " ")
			}
		}
		part := &PreparedMedia{
			Type: event.EventSticker,
			MessageEventContent: &event.MessageEventContent{
				Body: body,
				Info: &event.FileInfo{
					MimeType: sticker.MimeType,
					Width:    sticker.Width,
					Height:   sticker.Height,
					Size:     int(sticker.FileSize),
					BridgedSticker: &event.BridgedSticker{
						Network: StickerSourceID,
						ID:      base64.StdEncoding.EncodeToString(sticker.FileHash),
						Emoji:   emoji,
						PackURL: canonicalURL,
					},
				},
			},
			TypeDescription: "sticker",
		}
		dbKey := database.Key(fmt.Sprintf("stickercache:%x", part.Info.BridgedSticker.ID))
		fixStickerDimensions(part.Info)
		var packed *event.ImagePackImage
		if mc.DirectMedia {
			dbKey = ""
			if part.Info.MimeType == "application/was" {
				part.Info.MimeType = "video/lottie+json"
			}
			part.URL, err = mc.Bridge.Matrix.GenerateContentURI(ctx, waid.MakeStickerPackMediaID(pack.StickerPackID, sticker.FileHash, userLoginID))
			if err != nil {
				panic(fmt.Errorf("failed to generate content URI: %w", err))
			}
		} else if cached := mc.Bridge.DB.KV.Get(ctx, dbKey); cached != "" {
			err = json.Unmarshal([]byte(cached), &packed)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal cached sticker data: %w", err)
			}
		} else {
			err = mc.reuploadWhatsAppAttachment(ctx, sticker, part)
			if err != nil {
				return nil, fmt.Errorf("failed to reupload sticker %q: %w", sticker.GetDirectPath(), err)
			}
		}
		if packed == nil {
			packed = &event.ImagePackImage{
				URL:  part.URL,
				Body: part.Body,
				Info: part.Info,
			}
			if dbKey != "" {
				data, _ := json.Marshal(packed)
				if data != nil {
					mc.Bridge.DB.KV.Set(ctx, dbKey, string(data))
				}
			}
		}
		content.Images[shortcode] = packed
	}

	return &bridgev2.ImportedImagePack{
		Content:   content,
		Extra:     topLevelExtra,
		Shortcode: pack.StickerPackID,
	}, nil
}

type StickerMetadata struct {
	StickerPackID       string   `json:"sticker-pack-id"`
	AccessibilityText   string   `json:"accessibility-text"`
	Emojis              []string `json:"emojis"`
	IsFirstPartySticker int      `json:"is-first-party-sticker"`
}

func (sm *StickerMetadata) ToMatrix(content *event.MessageEventContent) {
	if sm == nil {
		return
	}
	if sm.StickerPackID != "" && content.Info.BridgedSticker == nil {
		content.Info.BridgedSticker = &event.BridgedSticker{
			Network: StickerSourceID,
			PackURL: StickerPackURLPrefix + sm.StickerPackID,
		}
		if len(sm.Emojis) > 0 {
			content.Info.BridgedSticker.Emoji = sm.Emojis[0]
		}
	}
	if sm.AccessibilityText != "" {
		content.Body = sm.AccessibilityText
	} else if len(sm.Emojis) > 0 {
		content.Body = strings.Join(sm.Emojis, " ")
	}
}

const StickerSourceID = "whatsapp"
const StickerPackURLPrefix = "https://wa.me/stickerpack/"

func PackAnimatedSticker(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	f, err := zipWriter.Create("animation/animation.json")
	if err != nil {
		return nil, fmt.Errorf("failed to create zip entry: %w", err)
	}
	_, err = f.Write(data)
	if err != nil {
		return nil, fmt.Errorf("failed to write zip entry: %w", err)
	}
	err = zipWriter.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close zip writer: %w", err)
	}
	return buf.Bytes(), nil
}

func ExtractAnimatedSticker(data []byte) ([]byte, *StickerMetadata, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read sticker zip: %w", err)
	}
	animationFile, err := zipReader.Open("animation/animation.json")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open animation.json: %w", err)
	}
	animationFileInfo, err := animationFile.Stat()
	if err != nil {
		_ = animationFile.Close()
		return nil, nil, fmt.Errorf("failed to stat animation.json: %w", err)
	} else if animationFileInfo.Size() > uploadFileThreshold {
		_ = animationFile.Close()
		return nil, nil, fmt.Errorf("animation.json is too large (%.2f MiB)", float64(animationFileInfo.Size())/1024/1024)
	}
	data, err = io.ReadAll(animationFile)
	_ = animationFile.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read animation.json: %w", err)
	}
	var meta StickerMetadata
	metaFile, err := zipReader.Open("animation/animation.json.overridden_metadata")
	if err == nil {
		_ = json.NewDecoder(metaFile).Decode(&meta)
		_ = metaFile.Close()
	}
	if meta.StickerPackID == "" {
		res := gjson.GetBytes(data, "metadata.customProps")
		if res.IsObject() {
			_ = json.Unmarshal(exstrings.UnsafeBytes(res.Raw), &meta)
		}
	}
	return data, &meta, nil
}

func (mc *MessageConverter) extractAnimatedSticker(fileInfo *PreparedMedia, data []byte) ([]byte, error) {
	data, meta, err := ExtractAnimatedSticker(data)
	if err != nil {
		return nil, err
	}
	meta.ToMatrix(fileInfo.MessageEventContent)
	fileInfo.Info.MimeType = "video/lottie+json"
	fileInfo.FileName = "sticker.json"
	return data, nil
}

func (mc *MessageConverter) convertAnimatedSticker(ctx context.Context, fileInfo *PreparedMedia, data []byte) ([]byte, []byte, *event.FileInfo, error) {
	data, err := mc.extractAnimatedSticker(fileInfo, data)
	if err != nil {
		return nil, nil, nil, err
	}
	c := mc.AnimatedStickerConfig
	if c.Target == "disable" {
		return data, nil, nil, nil
	} else if !lottie.Supported() {
		zerolog.Ctx(ctx).Warn().Msg("Animated sticker conversion is enabled, but lottieconverter is not installed")
		return data, nil, nil, nil
	}
	input := bytes.NewReader(data)
	fileInfo.Info.MimeType = "image/" + c.Target
	fileInfo.FileName = "sticker." + c.Target
	switch c.Target {
	case "png":
		var output bytes.Buffer
		err = lottie.Convert(ctx, input, "", &output, c.Target, c.Args.Width, c.Args.Height, "1")
		return output.Bytes(), nil, nil, err
	case "gif":
		var output bytes.Buffer
		err = lottie.Convert(ctx, input, "", &output, c.Target, c.Args.Width, c.Args.Height, strconv.Itoa(c.Args.FPS))
		return output.Bytes(), nil, nil, err
	case "webm", "webp":
		tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("mautrix-whatsapp-lottieconverter-%s.%s", random.String(10), c.Target))
		defer func() {
			_ = os.Remove(tmpFile)
		}()
		thumbnailData, err := lottie.FFmpegConvert(ctx, input, tmpFile, c.Args.Width, c.Args.Height, c.Args.FPS)
		if err != nil {
			return nil, nil, nil, err
		}
		data, err = os.ReadFile(tmpFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to read converted file: %w", err)
		}
		var thumbnailInfo *event.FileInfo
		if thumbnailData != nil {
			thumbnailInfo = &event.FileInfo{
				MimeType: "image/png",
				Width:    c.Args.Width,
				Height:   c.Args.Height,
				Size:     len(thumbnailData),
			}
		}
		return data, thumbnailData, thumbnailInfo, nil
	default:
		return nil, nil, nil, fmt.Errorf("unsupported target format %s", c.Target)
	}
}

func (mc *MessageConverter) fillWebPStickerInfo(ctx context.Context, fileInfo *PreparedMedia, data []byte) {
	meta, err := extractWebPStickerMetadata(data)
	if err != nil {
		zerolog.Ctx(ctx).Debug().Err(err).Msg("Failed to extract webp sticker metadata")
		return
	}
	meta.ToMatrix(fileInfo.MessageEventContent)
}

// stickerMetadataEXIFTag is the custom EXIF tag WhatsApp uses to embed
// sticker pack metadata as a JSON object inside non-animated webp stickers.
const stickerMetadataEXIFTag = 0x5741

// extractWebPStickerMetadata parses the WhatsApp sticker pack metadata JSON
// embedded in EXIF tag 0x5741 of a non-animated webp sticker.
func extractWebPStickerMetadata(data []byte) (*StickerMetadata, error) {
	exif, err := findWebPChunk(data, "EXIF")
	if err != nil {
		return nil, err
	}
	raw, err := findEXIFTagValue(exif, stickerMetadataEXIFTag)
	if err != nil {
		return nil, err
	}
	var meta StickerMetadata
	err = json.Unmarshal(raw, &meta)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sticker metadata JSON: %w", err)
	}
	return &meta, nil
}

func findWebPChunk(data []byte, chunkType string) ([]byte, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, fmt.Errorf("not a webp file")
	}
	for pos := 12; pos+8 <= len(data); {
		size := binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		start := pos + 8
		end := start + int(size)
		if end > len(data) {
			return nil, fmt.Errorf("webp chunk %q extends past end of file", data[pos:pos+4])
		}
		if string(data[pos:pos+4]) == chunkType {
			return data[start:end], nil
		}
		pos = end
		if pos%2 != 0 {
			pos++
		}
	}
	return nil, fmt.Errorf("webp chunk %q not found", chunkType)
}

func findEXIFTagValue(exif []byte, tag uint16) ([]byte, error) {
	if len(exif) < 8 {
		return nil, fmt.Errorf("exif data too short")
	}
	var bo binary.ByteOrder
	switch string(exif[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("invalid TIFF byte order %q", exif[0:2])
	}
	if bo.Uint16(exif[2:4]) != 0x002A {
		return nil, fmt.Errorf("invalid TIFF magic")
	}
	ifdOffset := int(bo.Uint32(exif[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(exif) {
		return nil, fmt.Errorf("IFD offset out of range")
	}
	count := int(bo.Uint16(exif[ifdOffset : ifdOffset+2]))
	entries := ifdOffset + 2
	if entries+count*12 > len(exif) {
		return nil, fmt.Errorf("IFD entries out of range")
	}
	for i := 0; i < count; i++ {
		entry := exif[entries+i*12 : entries+(i+1)*12]
		if bo.Uint16(entry[0:2]) != tag {
			continue
		}
		// Tag 0x5741 stores JSON as type 7 (UNDEFINED), where size == count bytes.
		size := int(bo.Uint32(entry[4:8]))
		if size <= 4 {
			return entry[8 : 8+size], nil
		}
		offset := int(bo.Uint32(entry[8:12]))
		if offset+size > len(exif) {
			return nil, fmt.Errorf("exif tag value out of range")
		}
		return exif[offset : offset+size], nil
	}
	return nil, fmt.Errorf("exif tag 0x%04x not found", tag)
}
