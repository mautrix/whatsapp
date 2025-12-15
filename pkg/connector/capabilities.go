package connector

import (
	"context"
	"time"

	"go.mau.fi/util/ffmpeg"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var WhatsAppGeneralCaps = &bridgev2.NetworkGeneralCapabilities{
	DisappearingMessages: true,
	AggressiveUpdateInfo: true,
	ImplicitReadReceipts: true,
	Provisioning: bridgev2.ProvisioningCapabilities{
		ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
			CreateDM:    true,
			LookupPhone: true,
			ContactList: true,
		},
		GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
			"group": {
				TypeDescription: "a group chat",

				Name:         bridgev2.GroupFieldCapability{Allowed: true, MaxLength: 100},
				Disappear:    bridgev2.GroupFieldCapability{Allowed: true, DisappearSettings: waDisappearingCap},
				Participants: bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 1},
				Parent:       bridgev2.GroupFieldCapability{Allowed: true},
			},
		},
	},
}

var waDisappearingCap = &event.DisappearingTimerCapability{
	Types: []event.DisappearingType{event.DisappearingTypeAfterSend},
	Timers: []jsontime.Milliseconds{
		jsontime.MS(24 * time.Hour),      // 24 hours
		jsontime.MS(7 * 24 * time.Hour),  // 7 days
		jsontime.MS(90 * 24 * time.Hour), // 90 days
	},
}

func (wa *WhatsAppConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return WhatsAppGeneralCaps
}

func (wa *WhatsAppConnector) GetBridgeInfoVersion() (info, caps int) {
	return 1, 7
}

const WAMaxFileSize = 2000 * 1024 * 1024
const EditMaxAge = 15 * time.Minute
const MaxTextLength = 65536

func supportedIfFFmpeg() event.CapabilitySupportLevel {
	if ffmpeg.Supported() {
		return event.CapLevelPartialSupport
	}
	return event.CapLevelRejected
}

func capID() string {
	base := "fi.mau.whatsapp.capabilities.2025_12_15"
	if ffmpeg.Supported() {
		return base + "+ffmpeg"
	}
	return base
}

var whatsappCaps = &event.RoomFeatures{
	ID: capID(),

	Formatting: map[event.FormattingFeature]event.CapabilitySupportLevel{
		event.FmtBold:          event.CapLevelFullySupported,
		event.FmtItalic:        event.CapLevelFullySupported,
		event.FmtStrikethrough: event.CapLevelFullySupported,
		event.FmtInlineCode:    event.CapLevelFullySupported,
		event.FmtCodeBlock:     event.CapLevelFullySupported,
		event.FmtUserLink:      event.CapLevelFullySupported,
		event.FmtUnorderedList: event.CapLevelFullySupported,
		event.FmtOrderedList:   event.CapLevelFullySupported,
		event.FmtListStart:     event.CapLevelFullySupported,
		event.FmtBlockquote:    event.CapLevelFullySupported,

		event.FmtInlineLink: event.CapLevelPartialSupport,
		event.FmtHeaders:    event.CapLevelPartialSupport,
	},
	File: map[event.CapabilityMsgType]*event.FileFeatures{
		event.MsgImage: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/jpeg": event.CapLevelFullySupported,
				"image/png":  event.CapLevelPartialSupport,
				"image/webp": event.CapLevelPartialSupport,
				"image/gif":  supportedIfFFmpeg(),
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          WAMaxFileSize,
		},
		event.MsgAudio: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/mpeg": event.CapLevelFullySupported,
				"audio/mp4":  event.CapLevelFullySupported,
				"audio/ogg":  event.CapLevelFullySupported,
				"audio/aac":  event.CapLevelFullySupported,
				"audio/amr":  event.CapLevelFullySupported,
			},
			Caption: event.CapLevelDropped,
			MaxSize: WAMaxFileSize,
		},
		event.CapMsgVoice: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"audio/ogg; codecs=opus": event.CapLevelFullySupported,
				"audio/ogg":              event.CapLevelUnsupported,
			},
			Caption: event.CapLevelDropped,
			MaxSize: WAMaxFileSize,
		},
		event.CapMsgSticker: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/webp": event.CapLevelFullySupported,
				// TODO see if sending lottie is possible
				//"video/lottie+json": event.CapLevelFullySupported,
				"image/png":  event.CapLevelPartialSupport,
				"image/jpeg": event.CapLevelPartialSupport,
			},
			Caption: event.CapLevelDropped,
			MaxSize: WAMaxFileSize,
		},
		event.CapMsgGIF: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"video/mp4": event.CapLevelFullySupported,
				"image/gif": supportedIfFFmpeg(),
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          WAMaxFileSize,
		},
		event.MsgVideo: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"video/mp4":       event.CapLevelFullySupported,
				"video/3gpp":      event.CapLevelFullySupported,
				"video/webm":      supportedIfFFmpeg(),
				"video/quicktime": supportedIfFFmpeg(),
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          WAMaxFileSize,
		},
		event.MsgFile: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"*/*": event.CapLevelFullySupported,
			},
			Caption:          event.CapLevelFullySupported,
			MaxCaptionLength: MaxTextLength,
			MaxSize:          WAMaxFileSize,
		},
	},
	State: event.StateFeatureMap{
		event.StateRoomName.Type:                {Level: event.CapLevelFullySupported},
		event.StateRoomAvatar.Type:              {Level: event.CapLevelFullySupported},
		event.StateTopic.Type:                   {Level: event.CapLevelFullySupported},
		event.StateBeeperDisappearingTimer.Type: {Level: event.CapLevelFullySupported},
	},
	MemberActions: event.MemberFeatureMap{
		event.MemberActionInvite: event.CapLevelFullySupported,
		event.MemberActionKick:   event.CapLevelFullySupported,
		event.MemberActionLeave:  event.CapLevelFullySupported,
	},
	MaxTextLength:       MaxTextLength,
	LocationMessage:     event.CapLevelFullySupported,
	Poll:                event.CapLevelFullySupported,
	Reply:               event.CapLevelFullySupported,
	Edit:                event.CapLevelFullySupported,
	EditMaxAge:          ptr.Ptr(jsontime.S(EditMaxAge)),
	Delete:              event.CapLevelFullySupported,
	DeleteForMe:         false,
	DeleteMaxAge:        ptr.Ptr(jsontime.S(2 * 24 * time.Hour)),
	Reaction:            event.CapLevelFullySupported,
	ReactionCount:       1,
	ReadReceipts:        true,
	TypingNotifications: true,
	DisappearingTimer:   waDisappearingCap,
	DeleteChat:          true,
}

var whatsappDMCaps *event.RoomFeatures
var whatsappCAGCaps *event.RoomFeatures

func init() {
	whatsappDMCaps = ptr.Clone(whatsappCaps)
	whatsappDMCaps.ID = capID() + "+dm"
	whatsappDMCaps.State = event.StateFeatureMap{
		event.StateBeeperDisappearingTimer.Type: {Level: event.CapLevelFullySupported},
	}
	whatsappDMCaps.MemberActions = nil
	whatsappCAGCaps = ptr.Clone(whatsappCaps)
	whatsappCAGCaps.ID = capID() + "+cag"
	whatsappCAGCaps.Reply = event.CapLevelUnsupported
	whatsappCAGCaps.Thread = event.CapLevelFullySupported
}

func (wa *WhatsAppClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	if portal.Metadata.(*waid.PortalMetadata).CommunityAnnouncementGroup {
		return whatsappCAGCaps
	} else if portal.RoomType == database.RoomTypeDM {
		return whatsappDMCaps
	}
	return whatsappCaps
}
