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
	EncHash    []byte `json:"file_enc_sha256_b64"`
	Hash       []byte `json:"file_sha256_b64"`
	Key        []byte `json:"media_key_b64"`
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
	if len(image.EncHash) != 32 || len(image.Hash) != 32 || len(image.Key) != 32 {
		return nil
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(base64.StdEncoding.EncodeToString(image.Hash)),
		Get: func(ctx context.Context) ([]byte, error) {
			data, err := wa.Client.DownloadMediaWithPath(ctx, image.DirectPath, image.EncHash, image.Hash, image.Key, whatsmeow.MediaImage, "image", false)
			if err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to download Muse avatar")
				return nil, fmt.Errorf("failed to download Muse avatar: %w", err)
			}
			return data, nil
		},
	}
}
