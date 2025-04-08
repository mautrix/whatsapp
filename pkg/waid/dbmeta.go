// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package waid

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"

	"go.mau.fi/util/exerrors"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow/types"
)

type UserLoginMetadata struct {
	WADeviceID      uint16        `json:"wa_device_id"`
	WALID           string        `json:"wa_lid"`
	PhoneLastSeen   jsontime.Unix `json:"phone_last_seen"`
	PhoneLastPinged jsontime.Unix `json:"phone_last_pinged"`
	Timezone        string        `json:"timezone"`
	PushKeys        *PushKeys     `json:"push_keys,omitempty"`
	APNSEncPubKey   []byte        `json:"apns_enc_pubkey,omitempty"`
	APNSEncPrivKey  []byte        `json:"apns_enc_privkey,omitempty"`

	HistorySyncPortalsNeedCreating bool `json:"history_sync_portals_need_creating,omitempty"`
}

type PushKeys struct {
	P256DH  []byte `json:"p256dh"`
	Auth    []byte `json:"auth"`
	Private []byte `json:"private"`
}

func (m *UserLoginMetadata) GeneratePushKeys() {
	privateKey := exerrors.Must(ecdh.P256().GenerateKey(rand.Reader))
	m.PushKeys = &PushKeys{
		P256DH:  privateKey.Public().(*ecdh.PublicKey).Bytes(),
		Auth:    random.Bytes(16),
		Private: privateKey.Bytes(),
	}
}

type MessageErrorType string

const (
	MsgNoError             MessageErrorType = ""
	MsgErrDecryptionFailed MessageErrorType = "decryption_failed"
	MsgErrMediaNotFound    MessageErrorType = "media_not_found"
)

type GroupInviteMeta struct {
	JID        types.JID `json:"jid"`
	Code       string    `json:"code"`
	Expiration int64     `json:"expiration,string"`
	Inviter    types.JID `json:"inviter"`
}

type MessageMetadata struct {
	SenderDeviceID   uint16            `json:"sender_device_id,omitempty"`
	Error            MessageErrorType  `json:"error,omitempty"`
	BroadcastListJID *types.JID        `json:"broadcast_list_jid,omitempty"`
	GroupInvite      *GroupInviteMeta  `json:"group_invite,omitempty"`
	FailedMediaMeta  json.RawMessage   `json:"media_meta,omitempty"`
	DirectMediaMeta  json.RawMessage   `json:"direct_media_meta,omitempty"`
	IsMatrixPoll     bool              `json:"is_matrix_poll,omitempty"`
	Edits            []types.MessageID `json:"edits,omitempty"`
}

func (mm *MessageMetadata) CopyFrom(other any) {
	otherMM := other.(*MessageMetadata)
	mm.SenderDeviceID = otherMM.SenderDeviceID
	mm.Error = otherMM.Error
	if otherMM.BroadcastListJID != nil {
		mm.BroadcastListJID = otherMM.BroadcastListJID
	}
	if otherMM.FailedMediaMeta != nil {
		mm.FailedMediaMeta = otherMM.FailedMediaMeta
	}
	if otherMM.DirectMediaMeta != nil {
		mm.DirectMediaMeta = otherMM.DirectMediaMeta
	}
	if otherMM.GroupInvite != nil {
		mm.GroupInvite = otherMM.GroupInvite
	}
	mm.IsMatrixPoll = mm.IsMatrixPoll || otherMM.IsMatrixPoll
}

type ReactionMetadata struct {
	SenderDeviceID uint16 `json:"sender_device_id,omitempty"`
}

type PortalMetadata struct {
	DisappearingTimerSetAt     int64                `json:"disappearing_timer_set_at,omitempty"`
	LastSync                   jsontime.Unix        `json:"last_sync,omitempty"`
	CommunityAnnouncementGroup bool                 `json:"is_cag,omitempty"`
	AddressingMode             types.AddressingMode `json:"addressing_mode,omitempty"`
}

type GhostMetadata struct {
	LastSync jsontime.Unix `json:"last_sync,omitempty"`
}
