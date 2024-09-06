package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

var WhatsAppGeneralCaps = &bridgev2.NetworkGeneralCapabilities{
	DisappearingMessages: true,
	AggressiveUpdateInfo: false,
}

func (wa *WhatsAppConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return WhatsAppGeneralCaps
}

const WAMaxFileSize = 2000 * 1024 * 1024

var whatsappCaps = &bridgev2.NetworkRoomCapabilities{
	FormattedText:    true,
	UserMentions:     true,
	LocationMessages: true,
	Captions:         true,
	Replies:          true,
	Edits:            true,
	EditMaxCount:     10,
	EditMaxAge:       15 * time.Minute,
	Deletes:          true,
	DeleteMaxAge:     48 * time.Hour,
	DefaultFileRestriction: &bridgev2.FileRestriction{
		MaxSize: WAMaxFileSize,
	},
	Files: map[event.MessageType]bridgev2.FileRestriction{
		event.MsgImage: {
			MaxSize:   WAMaxFileSize,
			MimeTypes: []string{"image/png", "image/jpeg"},
		},
		event.MsgAudio: {
			MaxSize:   WAMaxFileSize,
			MimeTypes: []string{"audio/mpeg", "audio/mp4", "audio/ogg", "audio/aac", "audio/amr"},
		},
		event.MsgVideo: {
			MaxSize:   WAMaxFileSize,
			MimeTypes: []string{"video/mp4", "video/3gpp"},
		},
		event.MsgFile: {
			MaxSize: WAMaxFileSize,
		},
	},
	ReadReceipts:  true,
	Reactions:     true,
	ReactionCount: 1,
}

func (wa *WhatsAppClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *bridgev2.NetworkRoomCapabilities {
	return whatsappCaps
}
