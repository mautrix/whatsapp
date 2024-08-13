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
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (mc *MessageConverter) ToMatrix(ctx context.Context, portal *bridgev2.Portal, client *whatsmeow.Client, intent bridgev2.MatrixAPI, message *events.Message) *bridgev2.ConvertedMessage {
	cm := &bridgev2.ConvertedMessage{
		Parts: make([]*bridgev2.ConvertedMessagePart, 0),
	}

	if message.Message.GetAudioMessage() != nil {
		cm.Parts = append(cm.Parts)
	} else if message.Message.GetDocumentMessage() != nil {
		cm.Parts = append(cm.Parts)
	} else if message.Message.GetImageMessage() != nil {
		cm.Parts = append(cm.Parts)
	} else if message.Message.GetStickerMessage() != nil {
		cm.Parts = append(cm.Parts)
	} else if message.Message.GetVideoMessage() != nil {
		cm.Parts = append(cm.Parts)
	} else {

	}
	return cm
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

func (mc *MessageConverter) WhatsAppTextToMatrix(ctx context.Context, text string, mentions []string) *bridgev2.ConvertedMessagePart {
	content := &event.MessageEventContent{
		MsgType:  event.MsgText,
		Mentions: &event.Mentions{},
		Body:     text,
	}

	if len(mentions) > 0 {
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
	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
	}
}

/*func convertWhatsAppAttachment(
	ctx context.Context,
	mc *MessageConverter,
	msg *waE2E.Message,
) (media, caption *bridgev2.ConvertedMessagePart, err error) {
	if len(msg.GetCaption().GetText()) > 0 {
		caption = mc.WhatsAppTextToMatrix(ctx, msgWithCaption.GetCaption())
		caption.Content.MsgType = event.MsgText
	}
	metadata = typedTransport.GetAncillary()
	transport := typedTransport.GetIntegral().GetTransport()
	media, err = mc.reuploadWhatsAppAttachment(ctx, transport, mediaType, convert)
	return
}*/

func (mc *MessageConverter) reuploadWhatsAppAttachment(
	ctx context.Context,
	message whatsmeow.DownloadableMessage,
	mimeType string,
	client *whatsmeow.Client,
	intent bridgev2.MatrixAPI,
	portal *bridgev2.Portal,
) (*bridgev2.ConvertedMessagePart, error) {
	data, err := client.Download(message)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}
	var fileName string
	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, fileName, mimeType)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
	}
	return &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			Body: fileName,
			URL:  mxc,
			Info: &event.FileInfo{
				MimeType: mimeType,
				Size:     len(data),
			},
			File: file,
		},
		Extra: make(map[string]any),
	}, nil
}

/*
func (mc *MessageConverter) convertWhatsAppImage(ctx context.Context, image *waConsumerApplication.ConsumerApplication_ImageMessage) (converted, caption *bridgev2.ConvertedMessagePart, err error) {
	metadata, converted, caption, err := convertWhatsAppAttachment[*waMediaTransport.ImageTransport](ctx, mc, image, whatsmeow.MediaImage, func(ctx context.Context, data []byte, mimeType string) ([]byte, string, string, error) {
		fileName := "image" + exmime.ExtensionFromMimetype(mimeType)
		return data, mimeType, fileName, nil
	})
	if converted != nil {
		converted.Content.MsgType = event.MsgImage
		converted.Content.Info.Width = int(metadata.GetWidth())
		converted.Content.Info.Height = int(metadata.GetHeight())
	}
	return
}

func (mc *MessageConverter) convertWhatsAppSticker(ctx context.Context, sticker *waConsumerApplication.ConsumerApplication_StickerMessage) (converted, caption *bridgev2.ConvertedMessagePart, err error) {
	metadata, converted, caption, err := convertWhatsAppAttachment[*waMediaTransport.StickerTransport](ctx, mc, sticker, whatsmeow.MediaImage, func(ctx context.Context, data []byte, mimeType string) ([]byte, string, string, error) {
		fileName := "sticker" + exmime.ExtensionFromMimetype(mimeType)
		return data, mimeType, fileName, nil
	})
	if converted != nil {
		converted.Type = event.EventSticker
		converted.Content.Info.Width = int(metadata.GetWidth())
		converted.Content.Info.Height = int(metadata.GetHeight())
	}
	return
}

func (mc *MessageConverter) convertWhatsAppDocument(ctx context.Context, document *waConsumerApplication.ConsumerApplication_DocumentMessage) (converted, caption *bridgev2.ConvertedMessagePart, err error) {
	_, converted, caption, err = convertWhatsAppAttachment[*waMediaTransport.DocumentTransport](ctx, mc, document, whatsmeow.MediaDocument, func(ctx context.Context, data []byte, mimeType string) ([]byte, string, string, error) {
		fileName := document.GetFileName()
		if fileName == "" {
			fileName = "file" + exmime.ExtensionFromMimetype(mimeType)
		}
		return data, mimeType, fileName, nil
	})
	if converted != nil {
		converted.Content.MsgType = event.MsgFile
	}
	return
}

func (mc *MessageConverter) convertWhatsAppAudio(ctx context.Context, audio *waConsumerApplication.ConsumerApplication_AudioMessage) (converted, caption *bridgev2.ConvertedMessagePart, err error) {
	// Treat all audio messages as voice messages, official clients don't set the flag for some reason
	isVoiceMessage := true // audio.GetPTT()
	metadata, converted, caption, err := convertWhatsAppAttachment[*waMediaTransport.AudioTransport](ctx, mc, audio, whatsmeow.MediaAudio, func(ctx context.Context, data []byte, mimeType string) ([]byte, string, string, error) {
		fileName := "audio" + exmime.ExtensionFromMimetype(mimeType)
		if isVoiceMessage && !strings.HasPrefix(mimeType, "audio/ogg") {
			data, err = ffmpeg.ConvertBytes(ctx, data, ".ogg", []string{}, []string{"-c:a", "libopus"}, mimeType)
			if err != nil {
				return data, mimeType, fileName, fmt.Errorf("%w audio to ogg/opus: %w", bridgev2.ErrMediaConvertFailed, err)
			}
			fileName += ".ogg"
			mimeType = "audio/ogg"
		}
		return data, mimeType, fileName, nil
	})
	if converted != nil {
		converted.Content.MsgType = event.MsgAudio
		converted.Content.Info.Duration = int(metadata.GetSeconds() * 1000)
		if isVoiceMessage {
			converted.Content.MSC3245Voice = &event.MSC3245Voice{}
			converted.Content.MSC1767Audio = &event.MSC1767Audio{
				Duration: converted.Content.Info.Duration,
				Waveform: []int{},
			}
		}
	}
	return
}

func (mc *MessageConverter) convertWhatsAppVideo(ctx context.Context, video *waE2E.VideoMessage) (converted, caption *bridgev2.ConvertedMessagePart, err error) {
	metadata, converted, caption, err := convertWhatsAppAttachment[*waMediaTransport.VideoTransport](ctx, mc, video, whatsmeow.MediaVideo, func(ctx context.Context, data []byte, mimeType string) ([]byte, string, string, error) {
		fileName := "video" + exmime.ExtensionFromMimetype(mimeType)
		return data, mimeType, fileName, nil
	})
	if converted != nil {
		converted.Content.MsgType = event.MsgVideo
		converted.Content.Info.Width = int(metadata.GetWidth())
		converted.Content.Info.Height = int(metadata.GetHeight())
		converted.Content.Info.Duration = int(metadata.GetSeconds() * 1000)
		// FB is annoying and sends images in video containers sometimes
		if converted.Content.Info.MimeType == "image/gif" {
			converted.Content.MsgType = event.MsgImage
		} else if metadata.GetGifPlayback() {
			converted.Extra["info"] = map[string]any{
				"fi.mau.gif":           true,
				"fi.mau.loop":          true,
				"fi.mau.autoplay":      true,
				"fi.mau.hide_controls": true,
				"fi.mau.no_audio":      true,
			}
		}
	}
	return
}

func (mc *MessageConverter) convertWhatsAppMedia(ctx context.Context, rawContent *waConsumerApplication.ConsumerApplication_Content) (converted, caption *bridgev2.ConvertedMessagePart, err error) {
	switch content := rawContent.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage:
		return mc.convertWhatsAppImage(ctx, content.ImageMessage)
	case *waConsumerApplication.ConsumerApplication_Content_StickerMessage:
		return mc.convertWhatsAppSticker(ctx, content.StickerMessage)
	case *waConsumerApplication.ConsumerApplication_Content_ViewOnceMessage:
		switch realContent := content.ViewOnceMessage.GetViewOnceContent().(type) {
		case *waConsumerApplication.ConsumerApplication_ViewOnceMessage_ImageMessage:
			return mc.convertWhatsAppImage(ctx, realContent.ImageMessage)
		case *waConsumerApplication.ConsumerApplication_ViewOnceMessage_VideoMessage:
			return mc.convertWhatsAppVideo(ctx, realContent.VideoMessage)
		default:
			return nil, nil, fmt.Errorf("unrecognized view once message type %T", realContent)
		}
	case *waConsumerApplication.ConsumerApplication_Content_DocumentMessage:
		return mc.convertWhatsAppDocument(ctx, content.DocumentMessage)
	case *waConsumerApplication.ConsumerApplication_Content_AudioMessage:
		return mc.convertWhatsAppAudio(ctx, content.AudioMessage)
	case *waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		return mc.convertWhatsAppVideo(ctx, content.VideoMessage)
	default:
		return nil, nil, fmt.Errorf("unrecognized media message type %T", content)
	}
}*/

func (mc *MessageConverter) appName() string {
	return "WhatsApp app"
}

/*
func (mc *MessageConverter) waConsumerToMatrix(ctx context.Context, rawContent *waConsumerApplication.ConsumerApplication_Content) (parts []*bridgev2.ConvertedMessagePart) {
	parts = make([]*bridgev2.ConvertedMessagePart, 0, 2)
	switch content := rawContent.GetContent().(type) {
	case *waConsumerApplication.ConsumerApplication_Content_MessageText:
		parts = append(parts, mc.WhatsAppTextToMatrix(ctx, content.MessageText))
	case *waConsumerApplication.ConsumerApplication_Content_ExtendedTextMessage:
		part := mc.WhatsAppTextToMatrix(ctx, content.ExtendedTextMessage.GetText())
		// TODO convert url previews
		parts = append(parts, part)
	case *waConsumerApplication.ConsumerApplication_Content_ImageMessage,
		*waConsumerApplication.ConsumerApplication_Content_StickerMessage,
		*waConsumerApplication.ConsumerApplication_Content_ViewOnceMessage,
		*waConsumerApplication.ConsumerApplication_Content_DocumentMessage,
		*waConsumerApplication.ConsumerApplication_Content_AudioMessage,
		*waConsumerApplication.ConsumerApplication_Content_VideoMessage:
		converted, caption, err := mc.convertWhatsAppMedia(ctx, rawContent)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to convert media message")
			converted = &bridgev2.ConvertedMessagePart{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    "Failed to transfer media",
				},
			}
		}
		parts = append(parts, converted)
		if caption != nil {
			parts = append(parts, caption)
		}
	case *waConsumerApplication.ConsumerApplication_Content_LocationMessage:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgLocation,
				Body:    content.LocationMessage.GetLocation().GetName() + "\n" + content.LocationMessage.GetAddress(),
				GeoURI:  fmt.Sprintf("geo:%f,%f", content.LocationMessage.GetLocation().GetDegreesLatitude(), content.LocationMessage.GetLocation().GetDegreesLongitude()),
			},
		})
	case *waConsumerApplication.ConsumerApplication_Content_LiveLocationMessage:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgLocation,
				Body:    "Live location sharing started\n\nYou can see the location in the " + mc.appName(),
				GeoURI:  fmt.Sprintf("geo:%f,%f", content.LiveLocationMessage.GetLocation().GetDegreesLatitude(), content.LiveLocationMessage.GetLocation().GetDegreesLongitude()),
			},
		})
	case *waConsumerApplication.ConsumerApplication_Content_ContactMessage:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Unsupported message (contact)\n\nPlease open in " + mc.appName(),
			},
		})
	case *waConsumerApplication.ConsumerApplication_Content_ContactsArrayMessage:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Unsupported message (contacts array)\n\nPlease open in " + mc.appName(),
			},
		})
	default:
		zerolog.Ctx(ctx).Warn().Type("content_type", content).Msg("Unrecognized content type")
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("Unsupported message (%T)\n\nPlease open in %s", content, mc.appName()),
			},
		})
	}
	return
}

func (mc *MessageConverter) waLocationMessageToMatrix(ctx context.Context, content *waArmadilloXMA.ExtendedContentMessage, parsedURL *url.URL) (parts []*bridgev2.ConvertedMessagePart) {
	lat := parsedURL.Query().Get("lat")
	long := parsedURL.Query().Get("long")
	return []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgLocation,
			GeoURI:  fmt.Sprintf("geo:%s,%s", lat, long),
			Body:    fmt.Sprintf("%s\n%s", content.GetTitleText(), content.GetSubtitleText()),
		},
	}}
}

func (mc *MessageConverter) waExtendedContentMessageToMatrix(ctx context.Context, content *waArmadilloXMA.ExtendedContentMessage) (parts []*bridgev2.ConvertedMessagePart) {
	body := content.GetMessageText()
	for _, cta := range content.GetCtas() {
		parsedURL, err := url.Parse(cta.GetNativeURL())
		if err != nil {
			continue
		}
		if parsedURL.Scheme == "messenger" && parsedURL.Host == "location_share" {
			return mc.waLocationMessageToMatrix(ctx, content, parsedURL)
		}
		if parsedURL.Scheme == "https" && !strings.Contains(body, cta.GetNativeURL()) {
			if body == "" {
				body = cta.GetNativeURL()
			} else {
				body = fmt.Sprintf("%s\n\n%s", body, cta.GetNativeURL())
			}
		}
	}
	return []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		},
		Extra: map[string]any{
			"fi.mau.meta.temporary_unsupported_type": "Armadillo ExtendedContentMessage",
		},
	}}
}

func (mc *MessageConverter) waArmadilloToMatrix(ctx context.Context, rawContent *waArmadilloApplication.Armadillo_Content) (parts []*bridgev2.ConvertedMessagePart, replyOverride *waCommon.MessageKey) {
	parts = make([]*bridgev2.ConvertedMessagePart, 0, 2)
	switch content := rawContent.GetContent().(type) {
	case *waArmadilloApplication.Armadillo_Content_ExtendedContentMessage:
		return mc.waExtendedContentMessageToMatrix(ctx, content.ExtendedContentMessage), nil
	case *waArmadilloApplication.Armadillo_Content_BumpExistingMessage_:
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "Bumped a message",
			},
		})
		replyOverride = content.BumpExistingMessage.GetKey()
	//case *waArmadilloApplication.Armadillo_Content_RavenMessage_:
	//	// TODO
	default:
		zerolog.Ctx(ctx).Warn().Type("content_type", content).Msg("Unrecognized armadillo content type")
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("Unsupported message (%T)\n\nPlease open in %s", content, mc.appName()),
			},
		})
	}
	return
}

func (mc *MessageConverter) WhatsAppToMatrix(ctx context.Context, portal *bridgev2.Portal, client *whatsmeow.Client, intent bridgev2.MatrixAPI, evt *events.FBMessage) *bridgev2.ConvertedMessage {
	ctx = context.WithValue(ctx, contextKeyWAClient, client)
	ctx = context.WithValue(ctx, contextKeyIntent, intent)
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	cm := &bridgev2.ConvertedMessage{}

	var replyOverride *waCommon.MessageKey
	switch typedMsg := evt.Message.(type) {
	case *waConsumerApplication.ConsumerApplication:
		cm.Parts = mc.waConsumerToMatrix(ctx, typedMsg.GetPayload().GetContent())
	case *waArmadilloApplication.Armadillo:
		cm.Parts, replyOverride = mc.waArmadilloToMatrix(ctx, typedMsg.GetPayload().GetContent())
	default:
		cm.Parts = []*bridgev2.ConvertedMessagePart{{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Unsupported message content type",
			},
		}}
	}

	var sender id.UserID
	var replyTo id.EventID
	if qm := evt.Application.GetMetadata().GetQuotedMessage(); qm != nil {
		pcp, _ := types.ParseJID(qm.GetParticipant())
		// TODO what if participant is not set?
		cm.ReplyTo = &networkid.MessageOptionalPartID{
			MessageID: metaid.MakeWAMessageID(evt.Info.Chat, pcp, qm.GetStanzaID()),
		}
	} else if replyOverride != nil {
		pcp, _ := types.ParseJID(replyOverride.GetParticipant())
		// TODO what if participant is not set?
		cm.ReplyTo = &networkid.MessageOptionalPartID{
			MessageID: metaid.MakeWAMessageID(evt.Info.Chat, pcp, qm.GetStanzaID()),
		}
	}
	for _, part := range cm.Parts {
		if part.Content.Mentions == nil {
			part.Content.Mentions = &event.Mentions{}
		}
		if replyTo != "" {
			part.Content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(replyTo)
			if !slices.Contains(part.Content.Mentions.UserIDs, sender) {
				part.Content.Mentions.UserIDs = append(part.Content.Mentions.UserIDs, sender)
			}
		}
	}
	return cm
}
*/
