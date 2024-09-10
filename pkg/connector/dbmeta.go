package connector

import (
	"go.mau.fi/util/jsontime"
	"maunium.net/go/mautrix/bridgev2/database"
)

func (wa *WhatsAppConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Ghost: func() any {
			return &GhostMetadata{}
		},
		Message: func() any {
			return &MessageMetadata{}
		},
		Reaction: func() any {
			return &ReactionMetadata{}
		},
		Portal: func() any {
			return &PortalMetadata{}
		},
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}

type UserLoginMetadata struct {
	WADeviceID uint16 `json:"wa_device_id"`
	//TODO: Add phone last ping/seen
}

type MessageMetadata struct {
	SenderDeviceID uint16 `json:"sender_device_id,omitempty"`
}

type ReactionMetadata struct {
	SenderDeviceID uint16 `json:"sender_device_id,omitempty"`
}

type PortalMetadata struct {
	DisappearingTimerSetAt int64 `json:"disappearing_timer_set_at,omitempty"`
}

type GhostMetadata struct {
	AvatarFetchAttempted bool          `json:"avatar_fetch_attempted,omitempty"`
	LastSync             jsontime.Unix `json:"last_sync,omitempty"`
}
