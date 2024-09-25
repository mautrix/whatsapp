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
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exmime"
	"go.mau.fi/util/exslices"
	"go.mau.fi/util/lottie"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (mc *MessageConverter) convertMediaMessage(ctx context.Context, msg MediaMessage, typeName string) (part *bridgev2.ConvertedMessagePart, contextInfo *waE2E.ContextInfo) {
	mediaInfo := getMediaMessageFileInfo(msg)
	contextInfo = mediaInfo.ContextInfo
	var err error
	part, err = mc.reuploadWhatsAppAttachment(ctx, msg, mediaInfo)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload WhatsApp attachment")
		part = makeMediaFailure(typeName)
		return
	}
	part.Content.MsgType = mediaInfo.MsgType
	if mediaInfo.IsSticker {
		part.Type = event.EventSticker
	}

	if mediaInfo.Waveform != nil {
		part.Content.MSC3245Voice = &event.MSC3245Voice{}
		part.Content.MSC1767Audio = &event.MSC1767Audio{
			Duration: mediaInfo.Duration,
			Waveform: mediaInfo.Waveform,
		}
	}
	if mediaInfo.Caption != "" {
		part.Content.Body = mediaInfo.Caption
	}
	if mediaInfo.IsGif {
		part.Extra["info"] = map[string]any{
			"fi.mau.gif":           true,
			"fi.mau.loop":          true,
			"fi.mau.autoplay":      true,
			"fi.mau.hide_controls": true,
			"fi.mau.no_audio":      true,
		}
	}
	return
}

type MediaInfo struct {
	event.FileInfo
	FileName    string
	Waveform    []int
	IsGif       bool
	IsSticker   bool
	IsLottie    bool
	Caption     string
	MsgType     event.MessageType
	ContextInfo *waE2E.ContextInfo
}

type MediaMessage interface {
	whatsmeow.DownloadableMessage
	GetContextInfo() *waE2E.ContextInfo
	GetFileLength() uint64
	GetMimetype() string
}

type MediaMessageWithThumbnail interface {
	MediaMessage
	GetJPEGThumbnail() []byte
}

type MediaMessageWithCaption interface {
	MediaMessage
	GetCaption() string
}

type MediaMessageWithDimensions interface {
	MediaMessage
	GetHeight() uint32
	GetWidth() uint32
}

type MediaMessageWithFileName interface {
	MediaMessage
	GetFileName() string
}

type MediaMessageWithDuration interface {
	MediaMessage
	GetSeconds() uint32
}

func getMediaMessageFileInfo(rawMsg MediaMessage) *MediaInfo {
	info := &MediaInfo{}
	switch msg := rawMsg.(type) {
	case *waE2E.ImageMessage:
		info.MsgType = event.MsgImage
	case *waE2E.DocumentMessage:
		info.MsgType = event.MsgFile
		info.FileName = msg.GetFileName()
	case *waE2E.AudioMessage:
		info.MsgType = event.MsgAudio
		info.Waveform = exslices.CastFunc(msg.Waveform, func(from byte) int { return int(from) })
	case *waE2E.StickerMessage:
		info.IsSticker = true
		info.IsLottie = msg.GetIsLottie()
	case *waE2E.VideoMessage:
		info.MsgType = event.MsgVideo
		info.IsGif = msg.GetGifPlayback()
	}
	if durationMsg, ok := rawMsg.(MediaMessageWithDuration); ok {
		info.Duration = int(durationMsg.GetSeconds() * 1000)
	}
	if dimensionMsg, ok := rawMsg.(MediaMessageWithDimensions); ok {
		info.Width = int(dimensionMsg.GetWidth())
		info.Height = int(dimensionMsg.GetHeight())
	}
	if captionMsg, ok := rawMsg.(MediaMessageWithCaption); ok {
		info.Caption = captionMsg.GetCaption()
	}

	info.Size = int(rawMsg.GetFileLength())
	info.MimeType = rawMsg.GetMimetype()
	info.ContextInfo = rawMsg.GetContextInfo()
	return info
}

// TODO read this from config?
const uploadFileThreshold = 5 * 1024 * 1024

func (mc *MessageConverter) reuploadWhatsAppAttachment(
	ctx context.Context,
	message MediaMessage,
	fileInfo *MediaInfo,
) (*bridgev2.ConvertedMessagePart, error) {
	client := getClient(ctx)
	intent := getIntent(ctx)
	portal := getPortal(ctx)
	var mxc id.ContentURIString
	var file *event.EncryptedFileInfo
	var thumbnailData []byte
	var thumbnailInfo *event.FileInfo
	if fileInfo.Size > uploadFileThreshold {
		var err error
		mxc, file, err = intent.UploadMediaStream(ctx, portal.MXID, -1, true, func(file io.Writer) (*bridgev2.FileStreamResult, error) {
			err := client.DownloadToFile(message, file.(*os.File))
			if err != nil {
				return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
			}
			if fileInfo.MimeType == "" {
				header := make([]byte, 512)
				n, _ := file.(*os.File).ReadAt(header, 0)
				fileInfo.MimeType = http.DetectContentType(header[:n])
			}
			if fileInfo.FileName == "" {
				fileInfo.FileName = strings.TrimPrefix(string(fileInfo.MsgType), "m.") + exmime.ExtensionFromMimetype(fileInfo.MimeType)
			}
			return &bridgev2.FileStreamResult{
				FileName: fileInfo.FileName,
				MimeType: fileInfo.MimeType,
			}, nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		data, err := client.Download(message)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
		}
		if fileInfo.IsSticker && fileInfo.IsLottie && fileInfo.MimeType == "application/was" {
			data, thumbnailData, thumbnailInfo, err = mc.convertAnimatedSticker(ctx, fileInfo, data)
			if err != nil {
				return nil, err
			}
		}
		if fileInfo.MimeType == "" {
			fileInfo.MimeType = http.DetectContentType(data)
		}
		if fileInfo.FileName == "" {
			fileInfo.FileName = strings.TrimPrefix(string(fileInfo.MsgType), "m.") + exmime.ExtensionFromMimetype(fileInfo.MimeType)
		}
		mxc, file, err = intent.UploadMedia(ctx, portal.MXID, data, fileInfo.FileName, fileInfo.MimeType)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
		}
	}
	if thumbnailData != nil && thumbnailInfo != nil {
		var err error
		fileInfo.ThumbnailURL, fileInfo.ThumbnailFile, err = intent.UploadMedia(
			ctx,
			portal.MXID,
			thumbnailData,
			"thumbnail"+exmime.ExtensionFromMimetype(thumbnailInfo.MimeType),
			thumbnailInfo.MimeType,
		)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload thumbnail")
		} else {
			fileInfo.ThumbnailInfo = thumbnailInfo
		}
	}

	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:     fileInfo.FileName,
			FileName: fileInfo.FileName,
			Info:     &fileInfo.FileInfo,
			URL:      mxc,
			File:     file,
		},
		Extra: make(map[string]any),
	}, nil
}

func (mc *MessageConverter) extractAnimatedSticker(fileInfo *MediaInfo, data []byte) ([]byte, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to read sticker zip: %w", err)
	}
	animationFile, err := zipReader.Open("animation/animation.json")
	if err != nil {
		return nil, fmt.Errorf("failed to open animation.json: %w", err)
	}
	animationFileInfo, err := animationFile.Stat()
	if err != nil {
		_ = animationFile.Close()
		return nil, fmt.Errorf("failed to stat animation.json: %w", err)
	} else if animationFileInfo.Size() > uploadFileThreshold {
		_ = animationFile.Close()
		return nil, fmt.Errorf("animation.json is too large (%.2f MiB)", float64(animationFileInfo.Size())/1024/1024)
	}
	data, err = io.ReadAll(animationFile)
	_ = animationFile.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read animation.json: %w", err)
	}
	fileInfo.MimeType = "image/lottie+json"
	fileInfo.FileName = "sticker.json"
	return data, nil
}

func (mc *MessageConverter) convertAnimatedSticker(ctx context.Context, fileInfo *MediaInfo, data []byte) ([]byte, []byte, *event.FileInfo, error) {
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
	fileInfo.MimeType = "image/" + c.Target
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

func makeMediaFailure(mediaType string) *bridgev2.ConvertedMessagePart {
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("Failed to bridge %s, please view it on the WhatsApp app", mediaType),
		},
	}
}
