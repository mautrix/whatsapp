package waid

import (
	"go.mau.fi/util/jsontime"
	"go.mau.fi/whatsmeow/types"
)

type UserLoginMetadata struct {
	WADeviceID      uint16        `json:"wa_device_id"`
	PhoneLastSeen   jsontime.Unix `json:"phone_last_seen"`
	PhoneLastPinged jsontime.Unix `json:"phone_last_pinged"`
	Timezone        string        `json:"timezone"`
}

type MessageErrorType string

const (
	MsgNoError             MessageErrorType = ""
	MsgErrDecryptionFailed MessageErrorType = "decryption_failed"
	MsgErrMediaNotFound    MessageErrorType = "media_not_found"
)

type MessageMetadata struct {
	SenderDeviceID   uint16           `json:"sender_device_id,omitempty"`
	Error            MessageErrorType `json:"error,omitempty"`
	BroadcastListJID *types.JID       `json:"broadcast_list_jid,omitempty"`
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
