package msgconv

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
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
		return &waE2E.Message{
			StickerMessage: &waE2E.StickerMessage{
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
	case event.MsgAudio:
		return &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				PTT:           proto.Bool(content.MSC3245Voice != nil),
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
		return &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
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
		return &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{
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
	parseCtx.ReturnData["mentions"] = mentions
	parseCtx.ReturnData["portal"] = portal
	parsed := mc.HTMLParser.Parse(content.FormattedBody, parseCtx)

	if len(mentions) > 0 {
		contextInfo.MentionedJID = mentions
	}

	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(parsed),
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
		zerolog.Ctx(ctx.Ctx).Err(err).Str("mxid", mxid).Msg("Failed to get user for mention")
		return displayname
	} else if ghost != nil {
		jid, err := types.ParseJID(string(ghost.ID))
		if jid.User == "" || err != nil {
			return displayname
		}
	} else if user, err := mc.Bridge.GetExistingUserByMXID(ctx.Ctx, id.UserID(mxid)); err != nil {
		zerolog.Ctx(ctx.Ctx).Err(err).Str("mxid", mxid).Msg("Failed to get user for mention")
		return displayname
	} else if user != nil {
		portal := ctx.ReturnData["portal"].(*bridgev2.Portal)
		login, _, _ := portal.FindPreferredLogin(ctx.Ctx, user, false)
		if login == nil {
			return displayname
		}
		jid = waid.ParseWAUserLoginID(login.ID)
	} else {
		return displayname
	}
	mentions := ctx.ReturnData["mentions"].([]string)
	mentions = append(mentions, jid.String())
	return fmt.Sprintf("@%s", jid.User)
}

func (mc *MessageConverter) reuploadFileToWhatsApp(ctx context.Context, client *whatsmeow.Client, content *event.MessageEventContent) (*whatsmeow.UploadResponse, string, error) {
	mime := content.Info.MimeType
	fileName := content.Body
	if content.FileName != "" {
		fileName = content.FileName
	}
	data, err := mc.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)

	if mime == "" {
		mime = http.DetectContentType(data)
	}

	var mediaType whatsmeow.MediaType
	switch content.MsgType {
	case event.MsgImage, event.MessageType(event.EventSticker.Type):
		mediaType = whatsmeow.MediaImage
	case event.MsgVideo:
		mediaType = whatsmeow.MediaVideo
	case event.MsgAudio:
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
