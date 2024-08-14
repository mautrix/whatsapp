package msgconv

import (
	"context"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
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
}

func getMediaMessageFileInfo(msg *waE2E.Message) (message MediaMessage, info event.FileInfo, contextInfo *waE2E.ContextInfo, extraInfo ExtraMediaInfo) {
	info = event.FileInfo{}
	if msg.GetAudioMessage() != nil {
		message = msg.AudioMessage

		info.Duration = int(msg.AudioMessage.GetSeconds() * 1000)

		waveform, duration := getAudioMessageInfo(msg.AudioMessage)
		extraInfo.Waveform = waveform
		extraInfo.Duration = duration
	} else if msg.GetDocumentMessage() != nil {
		message = msg.DocumentMessage

		extraInfo.Caption = msg.DocumentMessage.GetCaption()
	} else if msg.GetImageMessage() != nil {
		message = msg.ImageMessage

		info.Width = int(msg.ImageMessage.GetWidth())
		info.Height = int(msg.ImageMessage.GetHeight())

		extraInfo.Caption = msg.DocumentMessage.GetCaption()
	} else if msg.GetStickerMessage() != nil {
		message = msg.StickerMessage

		info.Width = int(msg.StickerMessage.GetWidth())
		info.Height = int(msg.StickerMessage.GetHeight())
	} else if msg.GetVideoMessage() != nil {
		message = msg.VideoMessage

		info.Duration = int(msg.VideoMessage.GetSeconds() * 1000)
		info.Width = int(msg.VideoMessage.GetWidth())
		info.Height = int(msg.VideoMessage.GetHeight())

		extraInfo.Caption = msg.DocumentMessage.GetCaption()
		if msg.VideoMessage.GetGifPlayback() {
			extraInfo.IsGif = true
		}
	} else {
		message = nil
		info = event.FileInfo{}
		extraInfo = ExtraMediaInfo{}

		contextInfo = &waE2E.ContextInfo{}
	}

	info.Size = int(message.GetFileLength())
	info.MimeType = message.GetMimetype()

	extraInfo = ExtraMediaInfo{}
	contextInfo = message.GetContextInfo()
	return
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
				mxid, displayname, err := mc.getBasicUserInfo(ctx, waid.MakeWAUserID(&parsed))

				content.Mentions.UserIDs = append(content.Mentions.UserIDs, mxid)
				mentionText := "@" + parsed.User
				content.Body = strings.ReplaceAll(content.Body, mentionText, displayname)
				content.FormattedBody = strings.ReplaceAll(content.FormattedBody, mentionText, fmt.Sprintf(`<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), event.TextToHTML(displayname)))
			}
		}

		if qm := contextInfo.GetQuotedMessage(); qm != nil {
			pcp, _ := types.ParseJID(contextInfo.GetParticipant())
			chat, err := types.ParseJID(contextInfo.GetRemoteJID())
			if err != nil {
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

func (mc *MessageConverter) ToMatrix(ctx context.Context, portal *bridgev2.Portal, client *whatsmeow.Client, intent bridgev2.MatrixAPI, message *events.Message) *bridgev2.ConvertedMessage {
	cm := &bridgev2.ConvertedMessage{
		Parts: make([]*bridgev2.ConvertedMessagePart, 0),
	}

	media, fileInfo, contextInfo, extraInfo := getMediaMessageFileInfo(message.Message)
	if media != nil {
		cmp, err := mc.reuploadWhatsAppAttachment(ctx, media, fileInfo, client, intent, portal)
		if err != nil {
			// TODO: THROW SOME ERROR HERE!?
			return cm
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
	} else if location := message.Message.GetLocationMessage(); location != nil {
		cmp := &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgLocation,
				Body:    location.GetComment(),
				GeoURI:  fmt.Sprintf("geo:%f,%f", location.GetDegreesLatitude(), location.GetDegreesLongitude()),
			},
		}

		contextInfo = location.GetContextInfo()
		cm.Parts = append(cm.Parts, cmp)
	} else {
		cmp := &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
			},
		}
		if extendedText := message.Message.GetExtendedTextMessage(); extendedText != nil {
			cmp.Content.Body = extendedText.GetText()
			contextInfo = extendedText.GetContextInfo()
		} else if conversation := message.Message.GetConversation(); conversation != "" {
			cmp.Content.Body = conversation
			contextInfo = nil
		} else {
			cmp.Content.MsgType = event.MsgNotice
			cmp.Content.Body = "Unknown message type, please view it on the WhatsApp app"
		}
	}

	if contextInfo != nil {
		cm = mc.parseMessageContextInfo(ctx, cm, contextInfo, message.Info)
	}

	return cm
}
