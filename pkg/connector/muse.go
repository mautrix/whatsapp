package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

type museImage struct {
	DirectPath string `json:"direct_path"`
	EncHash    string `json:"file_enc_sha256_b64"`
	Hash       string `json:"file_sha256_b64"`
	Key        string `json:"media_key_b64"`
	Length     int    `json:"file_length"`
}

func (wa *WhatsAppClient) handleMuseMetadata(ctx context.Context, data []byte) bool {
	var msg struct {
		Type    string `json:"type"`
		Payload struct {
			Event   string `json:"event"`
			Payload struct {
				Name   string `json:"name"`
				Avatar struct {
					Image museImage `json:"secure_image"`
				} `json:"avatar"`
			} `json:"payload"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decode Muse metadata")
		return true
	}
	if msg.Type != "event" || msg.Payload.Event != "identity.updated" || msg.Payload.Payload.Name == "" {
		return true
	}
	name := msg.Payload.Payload.Name
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatInfoChange,
			PortalKey:    wa.makeWAPortalKey(types.MuseJID),
			CreatePortal: true,
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				Name:                       &name,
				Avatar:                     wa.museAvatar(msg.Payload.Payload.Avatar.Image),
				ExcludeChangesFromTimeline: true,
			},
		},
	}).Success
}

func (wa *WhatsAppClient) museAvatar(image museImage) *bridgev2.Avatar {
	if image.DirectPath == "" || image.Length <= 0 || image.Length > 5<<20 {
		return nil
	}
	encHash, e1 := base64.StdEncoding.DecodeString(image.EncHash)
	hash, e2 := base64.StdEncoding.DecodeString(image.Hash)
	key, e3 := base64.StdEncoding.DecodeString(image.Key)
	if e1 != nil || e2 != nil || e3 != nil || len(encHash) != 32 || len(hash) != 32 || len(key) != 32 {
		return nil
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(image.Hash),
		Get: func(ctx context.Context) ([]byte, error) {
			data, err := wa.Client.DownloadMediaWithPath(ctx, image.DirectPath, encHash, hash, key, whatsmeow.MediaImage, "image", false)
			if err != nil {
				zerolog.Ctx(ctx).Warn().Str("error_type", fmt.Sprintf("%T", err)).Msg("Failed to download Muse avatar")
				return nil, fmt.Errorf("failed to download Muse avatar: %T", err)
			}
			return data, nil
		},
	}
}
