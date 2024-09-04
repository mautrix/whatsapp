package msgconv

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

type MediaMessage interface {
	whatsmeow.DownloadableMessage
	GetContextInfo() *waE2E.ContextInfo
	GetFileLength() uint64
	GetMimetype() string
}

func getAudioMessageInfo(audioMessage *waE2E.AudioMessage) (waveform []int, duration int) {
	if audioMessage.Waveform != nil {
		waveform = make([]int, len(audioMessage.Waveform))
		maxWave := 0
		for i, part := range audioMessage.Waveform {
			waveform[i] = int(part)
			if waveform[i] > maxWave {
				maxWave = waveform[i]
			}
		}
		multiplier := 0
		if maxWave > 0 {
			multiplier = 1024 / maxWave
		}
		if multiplier > 32 {
			multiplier = 32
		}
		for i := range waveform {
			waveform[i] *= multiplier
		}
	}
	duration = int(audioMessage.GetSeconds() * 1000)
	return
}

func (mc *MessageConverter) getBasicUserInfo(ctx context.Context, user networkid.UserID) (id.UserID, string, error) {
	ghost, err := mc.Bridge.GetGhostByID(ctx, user)
	if err != nil {
		return "", "", fmt.Errorf("failed to get ghost by ID: %w", err)
	}
	login := mc.Bridge.GetCachedUserLoginByID(networkid.UserLoginID(user))
	if login != nil {
		return login.UserMXID, ghost.Name, nil
	}
	return ghost.Intent.GetMXID(), ghost.Name, nil
}

type ExtraMediaInfo struct {
	Waveform []int
	Duration int

	IsGif bool

	Caption string

	MsgType event.MessageType
}

func getMediaMessageFileInfo(msg *waE2E.Message) (message MediaMessage, info event.FileInfo, contextInfo *waE2E.ContextInfo, extraInfo ExtraMediaInfo) {
	message = nil
	info = event.FileInfo{}
	extraInfo = ExtraMediaInfo{}

	contextInfo = &waE2E.ContextInfo{}
	if msg.GetAudioMessage() != nil {
		message = msg.AudioMessage

		info.Duration = int(msg.AudioMessage.GetSeconds() * 1000)

		waveform, duration := getAudioMessageInfo(msg.AudioMessage)
		extraInfo.Waveform = waveform
		extraInfo.Duration = duration

		extraInfo.MsgType = event.MsgAudio
	} else if msg.GetDocumentMessage() != nil {
		message = msg.DocumentMessage

		extraInfo.Caption = msg.DocumentMessage.GetCaption()
		extraInfo.MsgType = event.MsgFile
	} else if msg.GetImageMessage() != nil {
		message = msg.ImageMessage

		info.Width = int(msg.ImageMessage.GetWidth())
		info.Height = int(msg.ImageMessage.GetHeight())

		extraInfo.Caption = msg.DocumentMessage.GetCaption()
		extraInfo.MsgType = event.MsgImage
	} else if msg.GetStickerMessage() != nil {
		message = msg.StickerMessage

		info.Width = int(msg.StickerMessage.GetWidth())
		info.Height = int(msg.StickerMessage.GetHeight())
		extraInfo.MsgType = event.MessageType(event.EventSticker.Type)
	} else if msg.GetVideoMessage() != nil {
		message = msg.VideoMessage

		info.Duration = int(msg.VideoMessage.GetSeconds() * 1000)
		info.Width = int(msg.VideoMessage.GetWidth())
		info.Height = int(msg.VideoMessage.GetHeight())

		extraInfo.Caption = msg.DocumentMessage.GetCaption()
		if msg.VideoMessage.GetGifPlayback() {
			extraInfo.IsGif = true
		}
		extraInfo.MsgType = event.MsgVideo
	}

	if message != nil {
		info.Size = int(message.GetFileLength())

		info.MimeType = message.GetMimetype()

		contextInfo = message.GetContextInfo()
	}
	return
}

func convertContactMessage(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, msg *waE2E.ContactMessage) (*bridgev2.ConvertedMessagePart, error) {
	fileName := fmt.Sprintf("%s.vcf", msg.GetDisplayName())
	data := []byte(msg.GetVcard())
	mimeType := "text/vcard"

	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, fileName, mimeType)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
	}

	cmp := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:     fileName,
			FileName: fileName,
			URL:      mxc,
			Info: &event.FileInfo{
				MimeType: mimeType,
				Size:     len(msg.GetVcard()),
			},
			File:    file,
			MsgType: event.MsgFile,
		},
		Extra: make(map[string]any),
	}

	return cmp, nil
}

func (mc *MessageConverter) parseMessageContextInfo(ctx context.Context, cm *bridgev2.ConvertedMessage, contextInfo *waE2E.ContextInfo, msgInfo types.MessageInfo) *bridgev2.ConvertedMessage {
	for _, part := range cm.Parts {
		content := part.Content
		content.Mentions = &event.Mentions{
			UserIDs: make([]id.UserID, 0),
		}

		if mentions := contextInfo.GetMentionedJID(); len(mentions) > 0 {
			content.Format = event.FormatHTML
			content.FormattedBody = event.TextToHTML(content.Body)
			for _, jid := range mentions {
				parsed, err := types.ParseJID(jid)
				if err != nil {
					zerolog.Ctx(ctx).Err(err).Str("jid", jid).Msg("Failed to parse mentioned JID")
					continue
				}
				mxid, displayname, err := mc.getBasicUserInfo(ctx, waid.MakeUserID(&parsed))
				if err != nil {
					zerolog.Ctx(ctx).Err(err).Str("jid", jid).Msg("Failed to get user info")
					continue
				}
				content.Mentions.UserIDs = append(content.Mentions.UserIDs, mxid)
				mentionText := "@" + parsed.User
				content.Body = strings.ReplaceAll(content.Body, mentionText, displayname)
				content.FormattedBody = strings.ReplaceAll(content.FormattedBody, mentionText, fmt.Sprintf(`<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), event.TextToHTML(displayname)))
			}
		}

		if qm := contextInfo.GetQuotedMessage(); qm != nil {
			pcp, _ := types.ParseJID(contextInfo.GetParticipant())
			chat, _ := types.ParseJID(contextInfo.GetRemoteJID())
			if chat.String() == "" {
				chat = msgInfo.Chat
			}

			cm.ReplyTo = &networkid.MessageOptionalPartID{
				MessageID: waid.MakeMessageID(chat, pcp, contextInfo.GetStanzaID()),
			}
		}
	}

	return cm
}

func (mc *MessageConverter) reuploadWhatsAppAttachment(
	ctx context.Context,
	message MediaMessage,
	fileInfo event.FileInfo,
	client *whatsmeow.Client,
	intent bridgev2.MatrixAPI,
	portal *bridgev2.Portal,
) (*bridgev2.ConvertedMessagePart, error) {
	data, err := client.Download(message)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}
	var fileName string
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, fileName, fileInfo.MimeType)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
	}

	cmp := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body:     fileName,
			FileName: fileName,
			URL:      mxc,
			Info:     &fileInfo,
			File:     file,
		},
		Extra: make(map[string]any),
	}

	return cmp, nil
}

func convertLocationMessage(ctx context.Context, intent bridgev2.MatrixAPI, portal *bridgev2.Portal, msg *waE2E.LocationMessage) *bridgev2.ConvertedMessagePart {
	url := msg.GetURL()
	if len(url) == 0 {
		url = fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude())
	}
	name := msg.GetName()
	if len(name) == 0 {
		latChar := 'N'
		if msg.GetDegreesLatitude() < 0 {
			latChar = 'S'
		}
		longChar := 'E'
		if msg.GetDegreesLongitude() < 0 {
			longChar = 'W'
		}
		name = fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(msg.GetDegreesLatitude()), latChar, math.Abs(msg.GetDegreesLongitude()), longChar)
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgLocation,
		Body:          fmt.Sprintf("Location: %s\n%s\n%s", name, msg.GetAddress(), url),
		Format:        event.FormatHTML,
		FormattedBody: fmt.Sprintf("Location: <a href='%s'>%s</a><br>%s", url, name, msg.GetAddress()),
		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude()),
	}

	if len(msg.GetJPEGThumbnail()) > 0 {
		thumbnailMime := http.DetectContentType(msg.GetJPEGThumbnail())
		uploadedThumbnail, _, _ := intent.UploadMedia(ctx, portal.MXID, msg.GetJPEGThumbnail(), "thumb.jpeg", thumbnailMime)
		if uploadedThumbnail != "" {
			cfg, _, _ := image.DecodeConfig(bytes.NewReader(msg.GetJPEGThumbnail()))
			content.Info = &event.FileInfo{
				ThumbnailInfo: &event.FileInfo{
					Size:     len(msg.GetJPEGThumbnail()),
					Width:    cfg.Width,
					Height:   cfg.Height,
					MimeType: thumbnailMime,
				},
				ThumbnailURL: uploadedThumbnail,
			}
		}
	}

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
	}
}

func (mc *MessageConverter) ToMatrix(ctx context.Context, portal *bridgev2.Portal, client *whatsmeow.Client, intent bridgev2.MatrixAPI, message *waE2E.Message, msgInfo *types.MessageInfo) *bridgev2.ConvertedMessage {
	cm := &bridgev2.ConvertedMessage{
		Parts: make([]*bridgev2.ConvertedMessagePart, 0),
	}

	media, fileInfo, contextInfo, extraInfo := getMediaMessageFileInfo(message)
	if media != nil {
		cmp, err := mc.reuploadWhatsAppAttachment(ctx, media, fileInfo, client, intent, portal)
		if err != nil {
			// TODO: THROW SOME ERROR HERE!?
			return cm
		}

		if extraInfo.MsgType != "" {
			cmp.Content.MsgType = extraInfo.MsgType
		}

		if extraInfo.Waveform != nil {
			cmp.Content.MSC3245Voice = &event.MSC3245Voice{}
			cmp.Content.MSC1767Audio = &event.MSC1767Audio{
				Duration: extraInfo.Duration,
				Waveform: extraInfo.Waveform,
			}
		}
		if extraInfo.Caption != "" {
			cmp.Content.Body = extraInfo.Caption
		}
		if extraInfo.IsGif {
			cmp.Extra["info"] = map[string]any{
				"fi.mau.gif":           true,
				"fi.mau.loop":          true,
				"fi.mau.autoplay":      true,
				"fi.mau.hide_controls": true,
				"fi.mau.no_audio":      true,
			}
		}
		cm.Parts = append(cm.Parts, cmp)
	} else if location := message.GetLocationMessage(); location != nil {
		cmp := convertLocationMessage(ctx, intent, portal, location)

		contextInfo = location.GetContextInfo()
		cm.Parts = append(cm.Parts, cmp)
	} else if contacts := message.GetContactMessage(); contacts != nil {
		cmp, err := convertContactMessage(ctx, intent, portal, contacts)
		if err != nil {
			// TODO: THROW SOME ERROR HERE!?
			return cm
		}

		contextInfo = contacts.GetContextInfo()
		cm.Parts = append(cm.Parts, cmp)
	} else {
		cmp := &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
			},
		}
		if extendedText := message.GetExtendedTextMessage(); extendedText != nil {
			cmp.Content.Body = extendedText.GetText()
			contextInfo = extendedText.GetContextInfo()
		} else if conversation := message.GetConversation(); conversation != "" {
			cmp.Content.Body = conversation
			contextInfo = nil
		} else {
			cmp.Content.MsgType = event.MsgNotice
			cmp.Content.Body = "Unknown message type, please view it on the WhatsApp app"
		}
		cm.Parts = append(cm.Parts, cmp)
	}

	if contextInfo != nil && msgInfo != nil {
		cm = mc.parseMessageContextInfo(ctx, cm, contextInfo, *msgInfo)
	}

	return cm
}
