package msgconv

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exmime"
	"go.mau.fi/util/exslices"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
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

type MediaInfo struct {
	event.FileInfo
	FileName    string
	Waveform    []int
	IsGif       bool
	IsSticker   bool
	Caption     string
	MsgType     event.MessageType
	ContextInfo *waE2E.ContextInfo
}

func getMediaMessageFileInfo(msg *waE2E.Message) (message MediaMessage, info MediaInfo) {
	if msg.AudioMessage != nil {
		info.MsgType = event.MsgAudio
		message = msg.AudioMessage

		info.Duration = int(msg.AudioMessage.GetSeconds() * 1000)
		info.Waveform = exslices.CastFunc(msg.AudioMessage.Waveform, func(from byte) int { return int(from) })
	} else if msg.DocumentMessage != nil {
		info.MsgType = event.MsgFile
		message = msg.DocumentMessage

		info.Caption = msg.DocumentMessage.GetCaption()
		info.FileName = msg.DocumentMessage.GetFileName()
	} else if msg.ImageMessage != nil {
		info.MsgType = event.MsgImage
		message = msg.ImageMessage

		info.Width = int(msg.ImageMessage.GetWidth())
		info.Height = int(msg.ImageMessage.GetHeight())
		info.Caption = msg.ImageMessage.GetCaption()
	} else if msg.StickerMessage != nil {
		message = msg.StickerMessage

		info.Width = int(msg.StickerMessage.GetWidth())
		info.Height = int(msg.StickerMessage.GetHeight())
		info.IsSticker = true
	} else if msg.VideoMessage != nil {
		info.MsgType = event.MsgVideo
		message = msg.VideoMessage

		info.Duration = int(msg.VideoMessage.GetSeconds() * 1000)
		info.Width = int(msg.VideoMessage.GetWidth())
		info.Height = int(msg.VideoMessage.GetHeight())
		info.Caption = msg.VideoMessage.GetCaption()
		info.IsGif = msg.VideoMessage.GetGifPlayback()
	} else if msg.PtvMessage != nil {
		info.MsgType = event.MsgVideo
		message = msg.PtvMessage

		info.Duration = int(msg.PtvMessage.GetSeconds() * 1000)
		info.Width = int(msg.PtvMessage.GetWidth())
		info.Height = int(msg.PtvMessage.GetHeight())
		info.Caption = msg.PtvMessage.GetCaption()
		info.IsGif = msg.PtvMessage.GetGifPlayback()
	} else {
		return
	}

	info.Size = int(message.GetFileLength())
	info.MimeType = message.GetMimetype()
	info.ContextInfo = message.GetContextInfo()
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

func (mc *MessageConverter) addMentions(ctx context.Context, mentionedJID []string, into *event.MessageEventContent) {
	if len(mentionedJID) == 0 {
		return
	}
	into.EnsureHasHTML()
	for _, jid := range mentionedJID {
		parsed, err := types.ParseJID(jid)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Str("jid", jid).Msg("Failed to parse mentioned JID")
			continue
		}
		mxid, displayname, err := mc.getBasicUserInfo(ctx, waid.MakeUserID(parsed))
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Str("jid", jid).Msg("Failed to get user info")
			continue
		}
		into.Mentions.UserIDs = append(into.Mentions.UserIDs, mxid)
		mentionText := "@" + parsed.User
		into.Body = strings.ReplaceAll(into.Body, mentionText, displayname)
		into.FormattedBody = strings.ReplaceAll(into.FormattedBody, mentionText, fmt.Sprintf(`<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), html.EscapeString(displayname)))
	}
}

// TODO read this from config?
const uploadFileThreshold = 5 * 1024 * 1024

func (mc *MessageConverter) reuploadWhatsAppAttachment(
	ctx context.Context,
	message MediaMessage,
	fileInfo *MediaInfo,
	client *whatsmeow.Client,
	intent bridgev2.MatrixAPI,
	portal *bridgev2.Portal,
) (*bridgev2.ConvertedMessagePart, error) {
	var mxc id.ContentURIString
	var file *event.EncryptedFileInfo
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
		if fileInfo.IsSticker && fileInfo.MimeType == "application/was" {
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
		thumbnailURL, thumbnailFile, err := intent.UploadMedia(ctx, portal.MXID, msg.GetJPEGThumbnail(), "thumb.jpeg", thumbnailMime)
		if err == nil {
			cfg, _, _ := image.DecodeConfig(bytes.NewReader(msg.GetJPEGThumbnail()))
			content.Info = &event.FileInfo{
				ThumbnailInfo: &event.FileInfo{
					Size:     len(msg.GetJPEGThumbnail()),
					Width:    cfg.Width,
					Height:   cfg.Height,
					MimeType: thumbnailMime,
				},
				ThumbnailURL:  thumbnailURL,
				ThumbnailFile: thumbnailFile,
			}
		}
	}

	return &bridgev2.ConvertedMessagePart{
		Type:    event.EventMessage,
		Content: content,
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

func (mc *MessageConverter) ToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	client *whatsmeow.Client,
	intent bridgev2.MatrixAPI,
	message *waE2E.Message,
	info *types.MessageInfo,
) *bridgev2.ConvertedMessage {
	var part *bridgev2.ConvertedMessagePart
	var contextInfo *waE2E.ContextInfo
	var err error
	media, mediaInfo := getMediaMessageFileInfo(message)
	if media != nil {
		contextInfo = mediaInfo.ContextInfo
		part, err = mc.reuploadWhatsAppAttachment(ctx, media, &mediaInfo, client, intent, portal)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to reupload WhatsApp attachment")
			part = makeMediaFailure("attachment")
		} else {
			part.Content.MsgType = mediaInfo.MsgType
			if message.StickerMessage != nil {
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
		}
	} else if location := message.GetLocationMessage(); location != nil {
		part = convertLocationMessage(ctx, intent, portal, location)
		contextInfo = location.GetContextInfo()
	} else if contacts := message.GetContactMessage(); contacts != nil {
		part, err = convertContactMessage(ctx, intent, portal, contacts)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to convert contact message")
			part = makeMediaFailure("contact message")
		}

		contextInfo = contacts.GetContextInfo()
	} else {
		part = &bridgev2.ConvertedMessagePart{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
			},
		}
		if extendedText := message.GetExtendedTextMessage(); extendedText != nil {
			part.Content.Body = extendedText.GetText()
			contextInfo = extendedText.GetContextInfo()
		} else if conversation := message.GetConversation(); conversation != "" {
			part.Content.Body = conversation
			contextInfo = nil
		} else {
			part.Content.MsgType = event.MsgNotice
			part.Content.Body = "Unknown message type, please view it on the WhatsApp app"
		}
	}
	// TODO lots of message types missing

	part.Content.Mentions = &event.Mentions{}
	part.DBMetadata = &waid.MessageMetadata{
		SenderDeviceID: info.Sender.Device,
	}
	if info.IsIncomingBroadcast() {
		part.DBMetadata.(*waid.MessageMetadata).BroadcastListJID = &info.Chat
		if part.Extra == nil {
			part.Extra = map[string]any{}
		}
		part.Extra["fi.mau.whatsapp.source_broadcast_list"] = info.Chat.String()
	}
	mc.addMentions(ctx, contextInfo.GetMentionedJID(), part.Content)

	cm := &bridgev2.ConvertedMessage{
		Parts:     []*bridgev2.ConvertedMessagePart{part},
		Disappear: database.DisappearingSetting{},
	}
	if contextInfo.GetStanzaID() != "" {
		pcp, _ := types.ParseJID(contextInfo.GetParticipant())
		chat, _ := types.ParseJID(contextInfo.GetRemoteJID())
		if chat.IsEmpty() {
			chat, _ = waid.ParsePortalID(portal.ID)
		}
		cm.ReplyTo = &networkid.MessageOptionalPartID{
			MessageID: waid.MakeMessageID(chat, pcp, contextInfo.GetStanzaID()),
		}
	}

	return cm
}
