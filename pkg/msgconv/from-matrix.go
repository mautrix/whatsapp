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
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ffmpeg"
	"go.mau.fi/util/ptr"
	"go.mau.fi/util/random"
	cwebp "go.mau.fi/webp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) generateContextInfo(ctx context.Context, replyTo *database.Message, portal *bridgev2.Portal) *waE2E.ContextInfo {
	contextInfo := &waE2E.ContextInfo{}
	if replyTo != nil {
		msgID, err := waid.ParseMessageID(replyTo.ID)
		if err == nil {
			contextInfo.StanzaID = proto.String(msgID.ID)
			contextInfo.Participant = proto.String(msgID.Sender.String())
			contextInfo.QuotedMessage = &waE2E.Message{Conversation: proto.String("")}
		} else {
			zerolog.Ctx(ctx).Warn().Err(err).
				Stringer("reply_to_event_id", replyTo.MXID).
				Str("reply_to_message_id", string(replyTo.ID)).
				Msg("Failed to parse reply to message ID")
		}
	}
	if portal.Disappear.Timer > 0 {
		contextInfo.Expiration = ptr.Ptr(uint32(portal.Disappear.Timer.Seconds()))
		setAt := portal.Metadata.(*waid.PortalMetadata).DisappearingTimerSetAt
		if setAt > 0 {
			contextInfo.EphemeralSettingTimestamp = ptr.Ptr(setAt)
		}
	}
	return contextInfo
}

func (mc *MessageConverter) ToWhatsApp(
	ctx context.Context,
	client *whatsmeow.Client,
	evt *event.Event,
	content *event.MessageEventContent,
	replyTo,
	threadRoot *database.Message,
	portal *bridgev2.Portal,
) (*waE2E.Message, *whatsmeow.SendRequestExtra, error) {
	ctx = context.WithValue(ctx, contextKeyClient, client)
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	if evt.Type == event.EventSticker {
		content.MsgType = event.MessageType(event.EventSticker.Type)
	}

	message := &waE2E.Message{}
	contextInfo := mc.generateContextInfo(ctx, replyTo, portal)

	switch content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		message = mc.constructTextMessage(ctx, content, contextInfo)
	case event.MessageType(event.EventSticker.Type), event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
		uploaded, thumbnail, mime, err := mc.reuploadFileToWhatsApp(ctx, content)
		if err != nil {
			return nil, nil, err
		}
		message = mc.constructMediaMessage(ctx, content, evt, uploaded, thumbnail, contextInfo, mime)
	case event.MsgLocation:
		lat, long, err := parseGeoURI(content.GeoURI)
		if err != nil {
			return nil, nil, err
		}
		message.LocationMessage = &waE2E.LocationMessage{
			DegreesLatitude:  &lat,
			DegreesLongitude: &long,
			Comment:          &content.Body,
			ContextInfo:      contextInfo,
		}
	default:
		return nil, nil, fmt.Errorf("%w %s", bridgev2.ErrUnsupportedMessageType, content.MsgType)
	}
	extra := &whatsmeow.SendRequestExtra{}
	if portal.Metadata.(*waid.PortalMetadata).CommunityAnnouncementGroup {
		if threadRoot != nil {
			parsedID, err := waid.ParseMessageID(threadRoot.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse message ID: %w", err)
			}
			rootMsgInfo := MessageIDToInfo(client, parsedID)
			message, err = client.EncryptComment(ctx, rootMsgInfo, message)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to encrypt comment: %w", err)
			}
			lid := parsedID.Sender
			if lid.Server == types.DefaultUserServer {
				lid, err = client.Store.LIDs.GetLIDForPN(ctx, parsedID.Sender)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to get LID for PN: %w", err)
				}
			}
			extra.Meta = &types.MsgMetaInfo{
				ThreadMessageID:        parsedID.ID,
				ThreadMessageSenderJID: lid,
				DeprecatedLIDSession:   ptr.Ptr(false),
			}
		} else {
			// TODO check permissions?
			message.MessageContextInfo = &waE2E.MessageContextInfo{
				MessageSecret: random.Bytes(32),
			}
		}
	}
	return message, extra, nil
}

func (mc *MessageConverter) constructMediaMessage(
	ctx context.Context,
	content *event.MessageEventContent,
	evt *event.Event,
	uploaded *whatsmeow.UploadResponse,
	thumbnail []byte,
	contextInfo *waE2E.ContextInfo,
	mime string,
) *waE2E.Message {
	var caption string
	if content.FileName != "" && content.Body != content.FileName {
		caption, contextInfo.MentionedJID = mc.parseText(ctx, content)
	}
	switch content.MsgType {
	case event.MessageType(event.EventSticker.Type):
		width := uint32(content.Info.Width)
		height := uint32(content.Info.Height)

		return &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				Width:  &width,
				Height: &height,

				ContextInfo:   contextInfo,
				PngThumbnail:  thumbnail,
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mime),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				URL:           proto.String(uploaded.URL),
			},
		}
	case event.MsgAudio:
		waveform, seconds := getAudioInfo(content)

		return &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				Seconds:  &seconds,
				Waveform: waveform,
				PTT:      proto.Bool(content.MSC3245Voice != nil),

				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mime),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				ContextInfo:   contextInfo,
			},
		}
	case event.MsgImage:
		width := uint32(content.Info.Width)
		height := uint32(content.Info.Height)

		return &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Width:  &width,
				Height: &height,

				Caption:       proto.String(caption),
				JPEGThumbnail: thumbnail,
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mime),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				ContextInfo:   contextInfo,
			},
		}
	case event.MsgVideo:
		isGIF := mime == "video/mp4" && (content.Info.MimeType == "image/gif" || content.Info.MauGIF)

		width := uint32(content.Info.Width)
		height := uint32(content.Info.Height)
		seconds := uint32(content.Info.Duration / 1000)

		return &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				GifPlayback: proto.Bool(isGIF),
				Width:       &width,
				Height:      &height,
				Seconds:     &seconds,

				Caption:       proto.String(caption),
				JPEGThumbnail: thumbnail,
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mime),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				ContextInfo:   contextInfo,
			},
		}
	case event.MsgFile:
		msg := &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				FileName: proto.String(content.FileName),

				Caption:       proto.String(caption),
				JPEGThumbnail: thumbnail,
				URL:           proto.String(uploaded.URL),
				DirectPath:    proto.String(uploaded.DirectPath),
				MediaKey:      uploaded.MediaKey,
				Mimetype:      proto.String(mime),
				FileEncSHA256: uploaded.FileEncSHA256,
				FileSHA256:    uploaded.FileSHA256,
				FileLength:    proto.Uint64(uploaded.FileLength),
				ContextInfo:   contextInfo,
			},
		}
		if msg.GetDocumentMessage().GetCaption() != "" {
			msg.DocumentWithCaptionMessage = &waE2E.FutureProofMessage{
				Message: &waE2E.Message{
					DocumentMessage: msg.DocumentMessage,
				},
			}
			msg.DocumentMessage = nil
		}
		return msg
	default:
		return nil
	}
}

func (mc *MessageConverter) parseText(ctx context.Context, content *event.MessageEventContent) (text string, mentions []string) {
	mentions = make([]string, 0)

	parseCtx := format.NewContext(ctx)
	parseCtx.ReturnData["allowed_mentions"] = content.Mentions
	parseCtx.ReturnData["output_mentions"] = &mentions
	if content.Format == event.FormatHTML {
		text = mc.HTMLParser.Parse(content.FormattedBody, parseCtx)
	} else {
		text = content.Body
	}
	return
}

func (mc *MessageConverter) constructTextMessage(ctx context.Context, content *event.MessageEventContent, contextInfo *waE2E.ContextInfo) *waE2E.Message {
	text, mentions := mc.parseText(ctx, content)
	if len(mentions) > 0 {
		contextInfo.MentionedJID = mentions
	}
	etm := &waE2E.ExtendedTextMessage{
		Text:        proto.String(text),
		ContextInfo: contextInfo,
	}
	mc.convertURLPreviewToWhatsApp(ctx, content, etm)

	return &waE2E.Message{ExtendedTextMessage: etm}
}

func (mc *MessageConverter) convertPill(displayname, mxid, eventID string, ctx format.Context) string {
	if len(mxid) == 0 || mxid[0] != '@' {
		return format.DefaultPillConverter(displayname, mxid, eventID, ctx)
	}
	allowedMentions, _ := ctx.ReturnData["allowed_mentions"].(*event.Mentions)
	if allowedMentions != nil && !allowedMentions.Has(id.UserID(mxid)) {
		return displayname
	}
	var jid types.JID
	ghost, err := mc.Bridge.GetGhostByMXID(ctx.Ctx, id.UserID(mxid))
	if err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Str("mxid", mxid).Msg("Failed to get ghost for mention")
		return displayname
	} else if ghost != nil {
		jid = waid.ParseUserID(ghost.ID)
	} else if user, err := mc.Bridge.GetExistingUserByMXID(ctx.Ctx, id.UserID(mxid)); err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Str("mxid", mxid).Msg("Failed to get user for mention")
		return displayname
	} else if user != nil {
		portal := getPortal(ctx.Ctx)
		login, _, _ := portal.FindPreferredLogin(ctx.Ctx, user, false)
		if login == nil {
			return displayname
		}
		jid = waid.ParseUserLoginID(login.ID, 0)
	} else {
		return displayname
	}
	mentions := ctx.ReturnData["output_mentions"].(*[]string)
	*mentions = append(*mentions, jid.String())
	return fmt.Sprintf("@%s", jid.User)
}

type PaddedImage struct {
	image.Image
	Size       int
	OffsetX    int
	OffsetY    int
	RealWidth  int
	RealHeight int
}

func (img *PaddedImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, img.Size, img.Size)
}

func (img *PaddedImage) At(x, y int) color.Color {
	if x < img.OffsetX || y < img.OffsetY || x >= img.OffsetX+img.RealWidth || y >= img.OffsetY+img.RealHeight {
		return color.Transparent
	}
	return img.Image.At(x-img.OffsetX, y-img.OffsetY)
}

func (mc *MessageConverter) convertWebPtoPNG(webpImage []byte) ([]byte, error) {
	webpDecoded, err := webp.Decode(bytes.NewReader(webpImage))
	if err != nil {
		return nil, fmt.Errorf("failed to decode webp image: %w", err)
	}

	var pngBuffer bytes.Buffer
	if err = png.Encode(&pngBuffer, webpDecoded); err != nil {
		return nil, fmt.Errorf("failed to encode png image: %w", err)
	}

	return pngBuffer.Bytes(), nil
}

func (mc *MessageConverter) convertToWebP(img []byte) ([]byte, int, error) {
	decodedImg, _, err := image.Decode(bytes.NewReader(img))
	if err != nil {
		return img, 0, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := decodedImg.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var size int
	if width != height {
		paddedImg := &PaddedImage{
			Image:      decodedImg,
			OffsetX:    bounds.Min.Y,
			OffsetY:    bounds.Min.X,
			RealWidth:  width,
			RealHeight: height,
		}
		if width > height {
			size = width
			paddedImg.OffsetY += (size - height) / 2
		} else {
			size = height
			paddedImg.OffsetX += (size - width) / 2
		}
		paddedImg.Size = size
		decodedImg = paddedImg
	}

	var webpBuffer bytes.Buffer
	if err = cwebp.Encode(&webpBuffer, decodedImg, nil); err != nil {
		return img, 0, fmt.Errorf("failed to encode webp image: %w", err)
	}

	return webpBuffer.Bytes(), size, nil
}

func (mc *MessageConverter) reuploadFileToWhatsApp(
	ctx context.Context, content *event.MessageEventContent,
) (*whatsmeow.UploadResponse, []byte, string, error) {
	mime := content.GetInfo().MimeType
	fileName := content.Body
	if content.FileName != "" {
		fileName = content.FileName
	}
	data, err := mc.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}

	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if mime == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	var mediaType whatsmeow.MediaType
	var isSticker bool
	switch content.MsgType {
	case event.MessageType(event.EventSticker.Type):
		isSticker = true
		mediaType = whatsmeow.MediaImage
		if mime != "image/webp" || content.Info.Width != content.Info.Height {
			var size int
			data, size, err = mc.convertToWebP(data)
			if err != nil {
				return nil, nil, "image/webp", fmt.Errorf("%w (to webp): %w", bridgev2.ErrMediaConvertFailed, err)
			}
			content.Info.Width = size
			content.Info.Height = size
			mime = "image/webp"
		}
	case event.MsgImage:
		mediaType = whatsmeow.MediaImage
		switch mime {
		case "image/jpeg", "image/png":
			// allowed
		case "image/webp":
			data, err = mc.convertWebPtoPNG(data)
			if err != nil {
				return nil, nil, "image/webp", fmt.Errorf("%w (webp to png): %s", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "image/png"
		default:
			return nil, nil, mime, fmt.Errorf("%w %s in image message", bridgev2.ErrUnsupportedMediaType, mime)
		}
	case event.MsgVideo:
		switch mime {
		case "video/mp4", "video/3gpp":
			// allowed
		case "video/webm":
			data, err = ffmpeg.ConvertBytes(ctx, data, ".mp4", []string{"-f", "webm"}, []string{
				"-pix_fmt", "yuv420p", "-c:v", "libx264",
				"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
			}, mime)
			if err != nil {
				return nil, nil, "video/webm", fmt.Errorf("%w (webm to mp4): %w", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "video/mp4"
		case "image/gif":
			data, err = ffmpeg.ConvertBytes(ctx, data, ".mp4", []string{"-f", "gif"}, []string{
				"-pix_fmt", "yuv420p", "-c:v", "libx264", "-movflags", "+faststart",
				"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
			}, mime)
			if err != nil {
				return nil, nil, "image/gif", fmt.Errorf("%w (gif to mp4): %w", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "video/mp4"
		default:
			return nil, nil, mime, fmt.Errorf("%w %s in video message", bridgev2.ErrUnsupportedMediaType, mime)
		}
		mediaType = whatsmeow.MediaVideo
	case event.MsgAudio:
		switch mime {
		case "audio/aac", "audio/mp4", "audio/amr", "audio/mpeg", "audio/ogg; codecs=opus":
			// Allowed
		case "audio/ogg":
			// Hopefully it's opus already
			mime = "audio/ogg; codecs=opus"
		default:
			return nil, nil, mime, fmt.Errorf("%w %s in audio message", bridgev2.ErrUnsupportedMediaType, mime)
		}
		mediaType = whatsmeow.MediaAudio
	case event.MsgFile:
		fallthrough
	default:
		mediaType = whatsmeow.MediaDocument
	}

	uploaded, err := getClient(ctx).Upload(ctx, data, mediaType)
	if err != nil {
		zerolog.Ctx(ctx).Debug().
			Str("file_name", fileName).
			Str("mime_type", mime).
			Bool("is_voice_clip", content.MSC3245Voice != nil).
			Msg("Failed upload media")
		return nil, nil, "", fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
	}
	var thumbnail []byte
	// Audio doesn't have thumbnails
	if mediaType != whatsmeow.MediaAudio {
		thumbnail, err = mc.downloadThumbnail(ctx, data, content.GetInfo().ThumbnailURL, content.GetInfo().ThumbnailFile, isSticker)
		// Ignore format errors for non-image files, we don't care about those thumbnails
		if err != nil && (!errors.Is(err, image.ErrFormat) || mediaType == whatsmeow.MediaImage) {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to generate thumbnail for image message")
		}
	}
	return &uploaded, thumbnail, mime, nil
}

func parseGeoURI(uri string) (lat, long float64, err error) {
	if !strings.HasPrefix(uri, "geo:") {
		err = fmt.Errorf("uri doesn't have geo: prefix")
		return
	}
	// Remove geo: prefix and anything after ;
	coordinates := strings.Split(strings.TrimPrefix(uri, "geo:"), ";")[0]

	if splitCoordinates := strings.Split(coordinates, ","); len(splitCoordinates) != 2 {
		err = fmt.Errorf("didn't find exactly two numbers separated by a comma")
	} else if lat, err = strconv.ParseFloat(splitCoordinates[0], 64); err != nil {
		err = fmt.Errorf("latitude is not a number: %w", err)
	} else if long, err = strconv.ParseFloat(splitCoordinates[1], 64); err != nil {
		err = fmt.Errorf("longitude is not a number: %w", err)
	}
	return
}

func getAudioInfo(content *event.MessageEventContent) (output []byte, duration uint32) {
	duration = uint32(content.Info.Duration / 1000)
	audioInfo := content.MSC1767Audio
	if audioInfo == nil {
		return
	}
	if duration == 0 && audioInfo.Duration != 0 {
		duration = uint32(audioInfo.Duration / 1000)
	}
	waveform := audioInfo.Waveform
	if len(waveform) == 0 {
		return
	}

	maxVal := slices.Max(waveform)
	output = make([]byte, len(waveform))
	if maxVal <= 256 {
		for i, part := range waveform {
			output[i] = byte(min(part, 255))
		}
	} else {
		for i, part := range waveform {
			output[i] = byte(min(part/4, 255))
		}
	}
	return
}
