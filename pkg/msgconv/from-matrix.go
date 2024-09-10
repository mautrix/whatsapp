package msgconv

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ffmpeg"
	cwebp "go.mau.fi/webp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"golang.org/x/exp/slices"
	"golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) ToWhatsApp(
	ctx context.Context,
	client *whatsmeow.Client,
	evt *event.Event,
	content *event.MessageEventContent,
	replyTo *database.Message,
	portal *bridgev2.Portal,
) (*waE2E.Message, error) {
	if evt.Type == event.EventSticker {
		content.MsgType = event.MsgImage
	}

	message := &waE2E.Message{}
	contextInfo := &waE2E.ContextInfo{}

	if replyTo != nil {
		msgID, err := waid.ParseMessageID(replyTo.ID)
		if err == nil {
			contextInfo.StanzaID = proto.String(msgID.ID)
			contextInfo.RemoteJID = proto.String(msgID.Chat.String())
			contextInfo.Participant = proto.String(msgID.Sender.String())
			contextInfo.QuotedMessage = &waE2E.Message{Conversation: proto.String("")}
		} else {
			return nil, err
		}
	}

	switch content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		message = mc.constructTextMessage(ctx, content, portal, contextInfo)
	case event.MessageType(event.EventSticker.Type), event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
		uploaded, mime, err := mc.reuploadFileToWhatsApp(ctx, client, content)
		if err != nil {
			return nil, err
		}
		message = mc.constructMediaMessage(content, uploaded, contextInfo, mime)
	case event.MsgLocation:
		lat, long, err := parseGeoURI(content.GeoURI)
		if err != nil {
			return nil, err
		}
		message.LocationMessage = &waE2E.LocationMessage{
			DegreesLatitude:  &lat,
			DegreesLongitude: &long,
			Comment:          &content.Body,
			ContextInfo:      contextInfo,
		}
	default:
		return nil, fmt.Errorf("%w %s", bridgev2.ErrUnsupportedMessageType, content.MsgType)
	}
	return message, nil
}

func (mc *MessageConverter) constructMediaMessage(content *event.MessageEventContent, uploaded *whatsmeow.UploadResponse, contextInfo *waE2E.ContextInfo, mime string) *waE2E.Message {
	caption := content.Body
	switch content.MsgType {
	case event.MessageType(event.EventSticker.Type):
		width := uint32(content.Info.Width)
		height := uint32(content.Info.Height)

		return &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
				Width:  &width,
				Height: &height,

				ContextInfo:   contextInfo,
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
		isGif := false
		if mime == "video/mp4" && content.Info.MimeType == "image/gif" {
			isGif = true
		}

		width := uint32(content.Info.Width)
		height := uint32(content.Info.Height)
		seconds := uint32(content.Info.Duration / 1000)

		return &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
				GifPlayback: proto.Bool(isGif),
				Width:       &width,
				Height:      &height,
				Seconds:     &seconds,

				Caption:       proto.String(caption),
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
		return &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{
				FileName: proto.String(content.FileName),

				Caption:       proto.String(caption),
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
	default:
		return nil
	}
}

func (mc *MessageConverter) constructTextMessage(ctx context.Context, content *event.MessageEventContent, portal *bridgev2.Portal, contextInfo *waE2E.ContextInfo) *waE2E.Message {
	mentions := make([]string, 0)

	parseCtx := format.NewContext(ctx)
	parseCtx.ReturnData["mentions"] = &mentions
	parseCtx.ReturnData["portal"] = portal
	var text string
	if content.Format == event.FormatHTML {
		text = mc.HTMLParser.Parse(content.FormattedBody, parseCtx)
	} else {
		text = content.Body
	}

	if len(mentions) > 0 {
		contextInfo.MentionedJID = mentions
	}

	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: contextInfo,
		},
	}
}

func (mc *MessageConverter) convertPill(displayname, mxid, eventID string, ctx format.Context) string {
	if len(mxid) == 0 || mxid[0] != '@' {
		return format.DefaultPillConverter(displayname, mxid, eventID, ctx)
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
		portal := ctx.ReturnData["portal"].(*bridgev2.Portal)
		login, _, _ := portal.FindPreferredLogin(ctx.Ctx, user, false)
		if login == nil {
			return displayname
		}
		jid = waid.ParseUserLoginID(login.ID, 0)
	} else {
		return displayname
	}
	mentions := ctx.ReturnData["mentions"].(*[]string)
	*mentions = append(*mentions, jid.String())
	return fmt.Sprintf("@%s", jid.User)
}

type PaddedImage struct {
	image.Image
	Size    int
	OffsetX int
	OffsetY int
}

func (img *PaddedImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, img.Size, img.Size)
}

func (img *PaddedImage) At(x, y int) color.Color {
	return img.Image.At(x+img.OffsetX, y+img.OffsetY)
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

func (mc *MessageConverter) convertToWebP(img []byte) ([]byte, error) {
	decodedImg, _, err := image.Decode(bytes.NewReader(img))
	if err != nil {
		return img, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := decodedImg.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width != height {
		paddedImg := &PaddedImage{
			Image:   decodedImg,
			OffsetX: bounds.Min.Y,
			OffsetY: bounds.Min.X,
		}
		if width > height {
			paddedImg.Size = width
			paddedImg.OffsetY -= (paddedImg.Size - height) / 2
		} else {
			paddedImg.Size = height
			paddedImg.OffsetX -= (paddedImg.Size - width) / 2
		}
		decodedImg = paddedImg
	}

	var webpBuffer bytes.Buffer
	if err = cwebp.Encode(&webpBuffer, decodedImg, nil); err != nil {
		return img, fmt.Errorf("failed to encode webp image: %w", err)
	}

	return webpBuffer.Bytes(), nil
}

func (mc *MessageConverter) reuploadFileToWhatsApp(ctx context.Context, client *whatsmeow.Client, content *event.MessageEventContent) (*whatsmeow.UploadResponse, string, error) {
	mime := content.Info.MimeType
	fileName := content.Body
	if content.FileName != "" {
		fileName = content.FileName
	}
	data, err := mc.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}

	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if mime == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	var mediaType whatsmeow.MediaType
	switch content.MsgType {
	case event.MessageType(event.EventSticker.Type):
		mediaType = whatsmeow.MediaImage
		if mime != "image/webp" || content.Info.Width != content.Info.Height {
			data, err = mc.convertToWebP(data)
			if err != nil {
				return nil, "image/webp", fmt.Errorf("%w (to webp): %w", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "image/webp"
		}
	case event.MsgImage:
		switch mime {
		case "image/jpeg", "image/png":
			// allowed
		case "image/webp":
			data, err = mc.convertWebPtoPNG(data)
			if err != nil {
				return nil, "image/webp", fmt.Errorf("%w (webp to png): %s", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "image/png"
		default:
			return nil, mime, fmt.Errorf("%w %s in image message", bridgev2.ErrUnsupportedMediaType, mime)
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
				return nil, "video/webm", fmt.Errorf("%w (webm to mp4): %w", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "video/mp4"
		case "image/gif":
			data, err = ffmpeg.ConvertBytes(ctx, data, ".mp4", []string{"-f", "gif"}, []string{
				"-pix_fmt", "yuv420p", "-c:v", "libx264", "-movflags", "+faststart",
				"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
			}, mime)
			if err != nil {
				return nil, "image/gif", fmt.Errorf("%w (gif to mp4): %w", bridgev2.ErrMediaConvertFailed, err)
			}
			mime = "video/mp4"
		default:
			return nil, mime, fmt.Errorf("%w %s in video message", bridgev2.ErrUnsupportedMediaType, mime)
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
			return nil, mime, fmt.Errorf("%w %qs in audio message", bridgev2.ErrUnsupportedMediaType, mime)
		}
		mediaType = whatsmeow.MediaAudio
	case event.MsgFile:
		fallthrough
	default:
		mediaType = whatsmeow.MediaDocument
	}

	uploaded, err := client.Upload(ctx, data, mediaType)
	if err != nil {
		zerolog.Ctx(ctx).Debug().
			Str("file_name", fileName).
			Str("mime_type", mime).
			Bool("is_voice_clip", content.MSC3245Voice != nil).
			Msg("Failed upload metadata")
		return nil, "", fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
	}
	return &uploaded, mime, nil
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

func getAudioInfo(content *event.MessageEventContent) (output []byte, Duration uint32) {
	audioInfo := content.MSC1767Audio
	if audioInfo == nil {
		return nil, uint32(content.Info.Duration / 1000)
	}
	waveform := audioInfo.Waveform
	if waveform != nil {
		return nil, uint32(audioInfo.Duration / 1000)
	}

	maxVal := slices.Max(waveform)
	output = make([]byte, len(waveform))
	if maxVal < 256 {
		for i, part := range waveform {
			output[i] = byte(part)
		}
	} else {
		for i, part := range waveform {
			output[i] = min(byte(part/4), 255)
		}
	}
	return
}
