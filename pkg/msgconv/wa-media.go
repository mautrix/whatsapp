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
	"encoding/json"
	"errors"
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
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) convertMediaMessage(
	ctx context.Context,
	msg MediaMessage,
	typeName string,
	messageInfo *types.MessageInfo,
	isViewOnce bool,
	cachedPart *bridgev2.ConvertedMessagePart,
) (part *bridgev2.ConvertedMessagePart, contextInfo *waE2E.ContextInfo) {
	if mc.DisableViewOnce && isViewOnce {
		return &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("You received a view once %s. For added privacy, you can only open it on the WhatsApp app.", typeName),
			},
		}, nil
	}
	preparedMedia := prepareMediaMessage(msg)
	preparedMedia.TypeDescription = typeName
	if preparedMedia.FileName != "" && preparedMedia.Body != preparedMedia.FileName {
		mc.parseFormatting(preparedMedia.MessageEventContent, false, false)
	}
	contextInfo = preparedMedia.ContextInfo
	if cachedPart != nil && msg.GetDirectPath() == "" {
		cachedPart.Content.Body = preparedMedia.Body
		cachedPart.Content.Format = preparedMedia.Format
		cachedPart.Content.FormattedBody = preparedMedia.FormattedBody
		return cachedPart, contextInfo
	}
	mediaKeys := &FailedMediaKeys{
		Key:       msg.GetMediaKey(),
		Length:    msg.GetFileLength(),
		Type:      whatsmeow.GetMediaType(msg),
		SHA256:    msg.GetFileSHA256(),
		EncSHA256: msg.GetFileEncSHA256(),
	}
	if mc.DirectMedia {
		preparedMedia.FillFileName()
		var err error
		portal := getPortal(ctx)
		preparedMedia.URL, err = portal.Bridge.Matrix.GenerateContentURI(ctx, waid.MakeMediaID(messageInfo, portal.Receiver))
		if err != nil {
			panic(fmt.Errorf("failed to generate content URI: %w", err))
		}
		mediaKeys.DirectPath = msg.GetDirectPath()
		directMediaMeta, err := json.Marshal(mediaKeys)
		if err != nil {
			panic(err)
		}
		part = &bridgev2.ConvertedMessagePart{
			Type:    preparedMedia.Type,
			Content: preparedMedia.MessageEventContent,
			Extra:   preparedMedia.Extra,
			DBMetadata: &waid.MessageMetadata{
				DirectMediaMeta: directMediaMeta,
			},
		}
	} else if err := mc.reuploadWhatsAppAttachment(ctx, msg, preparedMedia); err != nil {
		part = mc.makeMediaFailure(ctx, preparedMedia, mediaKeys, err)
	} else {
		part = &bridgev2.ConvertedMessagePart{
			Type:    preparedMedia.Type,
			Content: preparedMedia.MessageEventContent,
			Extra:   preparedMedia.Extra,
		}
	}
	return
}

const FailedMediaField = "fi.mau.whatsapp.failed_media"

type FailedMediaKeys struct {
	Key        []byte              `json:"key"`
	Length     uint64              `json:"length"`
	Type       whatsmeow.MediaType `json:"type"`
	SHA256     []byte              `json:"sha256"`
	EncSHA256  []byte              `json:"enc_sha256"`
	DirectPath string              `json:"direct_path,omitempty"`
}

func (f *FailedMediaKeys) GetDirectPath() string {
	return f.DirectPath
}

func (f *FailedMediaKeys) GetMediaType() whatsmeow.MediaType {
	return f.Type
}

func (f *FailedMediaKeys) GetFileLength() uint64 {
	return f.Length
}

func (f *FailedMediaKeys) GetMediaKey() []byte {
	return f.Key
}

func (f *FailedMediaKeys) GetFileSHA256() []byte {
	return f.SHA256
}

func (f *FailedMediaKeys) GetFileEncSHA256() []byte {
	return f.EncSHA256
}

var (
	_ whatsmeow.DownloadableMessage = (*FailedMediaKeys)(nil)
	_ whatsmeow.MediaTypeable       = (*FailedMediaKeys)(nil)
)

type PreparedMedia struct {
	Type                       event.Type `json:"type"`
	*event.MessageEventContent `json:"content"`
	Extra                      map[string]any     `json:"extra"`
	FailedKeys                 *FailedMediaKeys   `json:"whatsapp_media"`          // only for failed media
	MentionedJID               []string           `json:"mentioned_jid,omitempty"` // only for failed media
	TypeDescription            string             `json:"type_description"`
	ContextInfo                *waE2E.ContextInfo `json:"-"`
}

func (pm *PreparedMedia) FillFileName() *PreparedMedia {
	if pm.FileName == "" {
		pm.FileName = strings.TrimPrefix(string(pm.MsgType), "m.") + exmime.ExtensionFromMimetype(pm.Info.MimeType)
	}
	return pm
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

func prepareMediaMessage(rawMsg MediaMessage) *PreparedMedia {
	extraInfo := map[string]any{}
	data := &PreparedMedia{
		Type: event.EventMessage,
		MessageEventContent: &event.MessageEventContent{
			Info: &event.FileInfo{},
		},
		Extra: map[string]any{
			"info": extraInfo,
		},
	}
	switch msg := rawMsg.(type) {
	case *waE2E.ImageMessage:
		data.MsgType = event.MsgImage
		data.FileName = "image" + exmime.ExtensionFromMimetype(msg.GetMimetype())
	case *waE2E.DocumentMessage:
		data.MsgType = event.MsgFile
		data.FileName = msg.GetFileName()
	case *waE2E.AudioMessage:
		data.MsgType = event.MsgAudio
		data.MSC1767Audio = &event.MSC1767Audio{
			Duration: int(msg.GetSeconds() * 1000),
			Waveform: exslices.CastFunc(msg.Waveform, func(from byte) int { return int(from) }),
		}
		data.FileName = "audio" + exmime.ExtensionFromMimetype(msg.GetMimetype())
		if msg.GetPTT() {
			data.MSC3245Voice = &event.MSC3245Voice{}
			data.FileName = "Voice message" + exmime.ExtensionFromMimetype(msg.GetMimetype())
		}
	case *waE2E.StickerMessage:
		data.Type = event.EventSticker
		data.FileName = "sticker" + exmime.ExtensionFromMimetype(msg.GetMimetype())
		if msg.GetMimetype() == "application/was" && data.FileName == "sticker" {
			data.FileName = "sticker.json"
		}
	case *waE2E.VideoMessage:
		data.MsgType = event.MsgVideo
		if msg.GetGifPlayback() {
			extraInfo["fi.mau.gif"] = true
			extraInfo["fi.mau.loop"] = true
			extraInfo["fi.mau.autoplay"] = true
			extraInfo["fi.mau.hide_controls"] = true
			extraInfo["fi.mau.no_audio"] = true
		}
		data.FileName = "video" + exmime.ExtensionFromMimetype(msg.GetMimetype())
	default:
		panic(fmt.Errorf("unknown media message type %T", rawMsg))
	}
	if durationMsg, ok := rawMsg.(MediaMessageWithDuration); ok {
		data.Info.Duration = int(durationMsg.GetSeconds() * 1000)
	}
	if dimensionMsg, ok := rawMsg.(MediaMessageWithDimensions); ok {
		data.Info.Width = int(dimensionMsg.GetWidth())
		data.Info.Height = int(dimensionMsg.GetHeight())
	}
	if captionMsg, ok := rawMsg.(MediaMessageWithCaption); ok && captionMsg.GetCaption() != "" {
		data.Body = captionMsg.GetCaption()
	} else {
		data.Body = data.FileName
	}

	data.Info.Size = int(rawMsg.GetFileLength())
	data.Info.MimeType = rawMsg.GetMimetype()
	data.ContextInfo = rawMsg.GetContextInfo()
	return data
}

// TODO read this from config?
const uploadFileThreshold = 5 * 1024 * 1024

func (mc *MessageConverter) MediaRetryToMatrix(
	ctx context.Context,
	part *PreparedMedia,
	client *whatsmeow.Client,
	intent bridgev2.MatrixAPI,
	portal *bridgev2.Portal,
	existingPart *database.Message,
) *bridgev2.ConvertedEdit {
	ctx = context.WithValue(ctx, contextKeyClient, client)
	ctx = context.WithValue(ctx, contextKeyIntent, intent)
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	err := mc.reuploadWhatsAppAttachment(ctx, part.FailedKeys, part)
	var updatedPart *bridgev2.ConvertedMessagePart
	if err != nil {
		updatedPart = mc.makeMediaFailure(ctx, part, nil, err)
	} else {
		// Event type can't be changed when editing, so turn stickers into images
		if part.Type == event.EventSticker {
			part.MsgType = event.MsgImage
		}
		updatedPart = &bridgev2.ConvertedMessagePart{
			Type:    event.EventMessage,
			Content: part.MessageEventContent,
			Extra:   part.Extra,
		}
	}
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{updatedPart.ToEditPart(existingPart)},
	}
}

func (mc *MessageConverter) reuploadWhatsAppAttachment(
	ctx context.Context,
	message whatsmeow.DownloadableMessage,
	part *PreparedMedia,
) error {
	client := getClient(ctx)
	intent := getIntent(ctx)
	portal := getPortal(ctx)
	var thumbnailData []byte
	var thumbnailInfo *event.FileInfo
	if part.Info.Size > uploadFileThreshold {
		var err error
		part.URL, part.File, err = intent.UploadMediaStream(ctx, portal.MXID, -1, true, func(file io.Writer) (*bridgev2.FileStreamResult, error) {
			err := client.DownloadToFile(message, file.(*os.File))
			if errors.Is(err, whatsmeow.ErrFileLengthMismatch) || errors.Is(err, whatsmeow.ErrInvalidMediaSHA256) {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Mismatching media checksums in message. Ignoring because WhatsApp seems to ignore them too")
			} else if err != nil {
				return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
			}
			if part.Info.MimeType == "" {
				header := make([]byte, 512)
				n, _ := file.(*os.File).ReadAt(header, 0)
				part.Info.MimeType = http.DetectContentType(header[:n])
			}
			part.FillFileName()
			return &bridgev2.FileStreamResult{
				FileName: part.FileName,
				MimeType: part.Info.MimeType,
			}, nil
		})
		if err != nil {
			return err
		}
	} else {
		data, err := client.Download(message)
		if errors.Is(err, whatsmeow.ErrFileLengthMismatch) || errors.Is(err, whatsmeow.ErrInvalidMediaSHA256) {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Mismatching media checksums in message. Ignoring because WhatsApp seems to ignore them too")
		} else if err != nil {
			return fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
		}
		if part.Type == event.EventSticker && part.Info.MimeType == "application/was" {
			data, thumbnailData, thumbnailInfo, err = mc.convertAnimatedSticker(ctx, part, data)
			if err != nil {
				return err
			}
		}
		if part.Info.MimeType == "" {
			part.Info.MimeType = http.DetectContentType(data)
		}
		part.FillFileName()
		part.URL, part.File, err = intent.UploadMedia(ctx, portal.MXID, data, part.FileName, part.Info.MimeType)
		if err != nil {
			return fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
		}
	}
	if thumbnailData != nil && thumbnailInfo != nil {
		var err error
		part.Info.ThumbnailURL, part.Info.ThumbnailFile, err = intent.UploadMedia(
			ctx,
			portal.MXID,
			thumbnailData,
			"thumbnail"+exmime.ExtensionFromMimetype(thumbnailInfo.MimeType),
			thumbnailInfo.MimeType,
		)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload thumbnail")
		} else {
			part.Info.ThumbnailInfo = thumbnailInfo
		}
	}
	return nil
}

func (mc *MessageConverter) extractAnimatedSticker(fileInfo *PreparedMedia, data []byte) ([]byte, error) {
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
	fileInfo.Info.MimeType = "image/lottie+json"
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

func (mc *MessageConverter) makeMediaFailure(ctx context.Context, mediaInfo *PreparedMedia, keys *FailedMediaKeys, err error) *bridgev2.ConvertedMessagePart {
	logLevel := zerolog.ErrorLevel
	var extra map[string]any
	var dbMeta any
	errorMsg := fmt.Sprintf("Failed to bridge %s, please view it on the WhatsApp app", mediaInfo.TypeDescription)
	if keys != nil && (errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)) {
		logLevel = zerolog.DebugLevel
		keys.DirectPath = ""
		mediaInfo.FailedKeys = keys
		mediaInfo.MentionedJID = mediaInfo.ContextInfo.GetMentionedJID()
		serializedMedia, serializerErr := json.Marshal(mediaInfo)
		if serializerErr != nil {
			zerolog.Ctx(ctx).Err(serializerErr).Msg("Failed to serialize media info")
		}
		extra = map[string]any{
			FailedMediaField: mediaInfo,
		}
		dbMeta = &waid.MessageMetadata{
			Error:           waid.MsgErrMediaNotFound,
			FailedMediaMeta: serializedMedia,
		}
		errorMsg = fmt.Sprintf("Old %s. %s", mediaInfo.TypeDescription, mc.OldMediaSuffix)
	}
	zerolog.Ctx(ctx).WithLevel(logLevel).Err(err).
		Str("media_type", mediaInfo.TypeDescription).
		Msg("Failed to reupload WhatsApp attachment")
	part := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    errorMsg,
		},
		Extra:      extra,
		DBMetadata: dbMeta,
	}
	if mediaInfo.FormattedBody != "" {
		part.Content.EnsureHasHTML()
		part.Content.FormattedBody += "<br><br>" + mediaInfo.FormattedBody
		part.Content.Body += "\n\n" + mediaInfo.Body
	} else if mediaInfo.Body != "" && mediaInfo.FileName != "" && mediaInfo.Body != mediaInfo.FileName {
		part.Content.Body += "\n\n" + mediaInfo.Body
	}
	return part
}
