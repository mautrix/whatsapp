package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exerrors"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

type museImage struct {
	DirectPath string `json:"direct_path"`
	EncHash    []byte `json:"file_enc_sha256_b64"`
	Hash       []byte `json:"file_sha256_b64"`
	Key        []byte `json:"media_key_b64"`
	Length     int    `json:"file_length"`
}

func (wa *WhatsAppClient) resyncWASARootSecrets(ctx context.Context) {
	defer func() {
		v := recover()
		if v != nil {
			zerolog.Ctx(ctx).Err(exerrors.RecoverToError(v)).
				Bytes("stack", debug.Stack()).
				Msg("Error resyncing WASA root secrets")
		}
	}()
	wa.wasaResyncLock.Lock()
	defer wa.wasaResyncLock.Unlock()
	if wa.offlineSyncWaiter.Load() != nil || !wa.Client.IsConnected() {
		return
	}
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	if meta.WASAResynced {
		return
	}
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Resyncing WASA root secrets for existing login")
	if err := wa.Client.FetchAppState(ctx, appstate.WAPatchRegularHigh, true, false); err != nil {
		log.Err(err).Msg("Failed to resync WASA root secrets")
		return
	}
	meta.WASAResynced = true
	if err := wa.UserLogin.Save(ctx); err != nil {
		meta.WASAResynced = false
		log.Err(err).Msg("Failed to save WASA resync completion")
		return
	}
	log.Info().Msg("Completed WASA root secret resync")
}

func (wa *WhatsAppClient) requestMuseProfile(ctx context.Context) {
	defer func() {
		v := recover()
		if v != nil {
			zerolog.Ctx(ctx).Err(exerrors.RecoverToError(v)).
				Bytes("stack", debug.Stack()).
				Msg("Error requesting Muse profile")
		}
	}()
	if !wa.museProfileLock.TryLock() {
		return
	}
	defer wa.museProfileLock.Unlock()
	if wa.offlineSyncWaiter.Load() != nil || !wa.Client.IsConnected() || time.Since(wa.lastMuseProfileRequest) < 5*time.Minute {
		return
	}
	portal, err := wa.Main.Bridge.DB.Portal.GetByKey(ctx, wa.makeWAPortalKey(types.MuseJID))
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to check Muse profile")
		return
	} else if portal != nil && portal.NameIsCustom && portal.Name != "" && portal.NameSet && museAvatarReady(portal) {
		return
	}
	rootID, err := wa.GetStore().ChatSettings.GetWASARootSecretID(ctx, types.MuseJID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to check Muse root secret")
		return
	} else if rootID == "" {
		return
	}
	wa.lastMuseProfileRequest = time.Now()
	_, err = wa.Client.SendMessage(ctx, types.MuseJID, &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_AI_METADATA_OPERATION.Enum(),
			AiMetadataOperation: &waAICommon.AIMetadataOperation{
				HatchMetadataSync: &waAICommon.HatchMetadataSync{
					Data:        []byte(`{"version":1,"type":"req","payload":{"method":"channel.bootstrap","params":{"sections":["agent.status","identity.updated","hitl.snapshot"]}}}`),
					TimestampMS: proto.Int64(time.Now().UnixMilli()),
					RequestID:   proto.String(uuid.NewString()),
				},
			},
		},
	})
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to request Muse profile")
	}
}

func museAvatarReady(portal *database.Portal) bool {
	expectedHash, err := base64.StdEncoding.DecodeString(string(portal.AvatarID))
	return err != nil || len(expectedHash) != 32 ||
		(portal.AvatarMXC != "" && portal.AvatarHash == [32]byte(expectedHash))
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
	avatar := wa.museAvatar(msg.Payload.Payload.Avatar.Image)
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatInfoChange,
			PortalKey:    wa.makeWAPortalKey(types.MuseJID),
			CreatePortal: true,
			PreHandleFunc: func(_ context.Context, portal *bridgev2.Portal) {
				if avatar != nil && !museAvatarReady(portal.Portal) {
					portal.AvatarSet = false
				}
			},
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				Name:                       &name,
				Avatar:                     avatar,
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
