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

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exsync"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var _ bridgev2.DirectMediableNetwork = (*WhatsAppConnector)(nil)

func (wa *WhatsAppConnector) SetUseDirectMedia() {
	wa.MsgConv.DirectMedia = true
}

var ErrReloadNeeded = mautrix.RespError{
	ErrCode:    "FI.MAU.WHATSAPP_RELOAD_NEEDED",
	Err:        "Media is no longer available on WhatsApp servers and must be re-requested from your phone",
	StatusCode: http.StatusNotFound,
}

func (wa *WhatsAppConnector) Download(ctx context.Context, mediaID networkid.MediaID, params map[string]string) (mediaproxy.GetMediaResponse, error) {
	parsedID, err := waid.ParseMediaID(mediaID)
	if err != nil {
		return nil, err
	}
	log := zerolog.Ctx(ctx).With().Any("parsed_media_id", parsedID).Logger()
	ctx = log.WithContext(ctx)
	if parsedID.Message != nil {
		return wa.downloadMessageDirectMedia(ctx, parsedID, params)
	} else if parsedID.Avatar != nil {
		return wa.downloadAvatarDirectMedia(ctx, parsedID, params)
	} else {
		return nil, fmt.Errorf("unexpected media ID parsing result")
	}
}

func (wa *WhatsAppConnector) downloadAvatarDirectMedia(ctx context.Context, parsedID *waid.ParsedMediaID, params map[string]string) (mediaproxy.GetMediaResponse, error) {
	ul := wa.Bridge.GetCachedUserLoginByID(parsedID.UserLogin)
	if ul == nil {
		return nil, fmt.Errorf("%w: user login %s not found", bridgev2.ErrNotLoggedIn, parsedID.UserLogin)
	}
	waClient := ul.Client.(*WhatsAppClient)
	if waClient.Client == nil {
		return nil, fmt.Errorf("no WhatsApp client found on login %s", parsedID.UserLogin)
	}
	cachedInfo, err := wa.DB.AvatarCache.Get(ctx, parsedID.Avatar.TargetJID, parsedID.Avatar.AvatarID)
	if err != nil {
		return nil, fmt.Errorf("failed to get avatar cache entry: %w", err)
	}
	if cachedInfo != nil && cachedInfo.Gone {
		return nil, mautrix.MNotFound.WithMessage("Avatar is no longer available")
	} else if cachedInfo == nil || cachedInfo.Expiry.Time.Before(time.Now().Add(5*time.Minute)) {
		zerolog.Ctx(ctx).Debug().
			Str("avatar_id", parsedID.Avatar.AvatarID).
			Msg("Refreshing avatar URL from WhatsApp servers")
		avatar, err := waClient.Client.GetProfilePictureInfo(parsedID.Avatar.TargetJID, &whatsmeow.GetProfilePictureParams{
			IsCommunity: parsedID.Avatar.Community,
		})
		if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) ||
			errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) ||
			(err == nil && (avatar == nil || avatar.ID != parsedID.Avatar.AvatarID)) {
			err = wa.DB.AvatarCache.Put(ctx, &wadb.AvatarCacheEntry{
				EntityJID: parsedID.Avatar.TargetJID,
				AvatarID:  parsedID.Avatar.AvatarID,
				Gone:      true,
			})
			if err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).
					Str("avatar_id", parsedID.Avatar.AvatarID).
					Msg("Failed to mark avatar as gone in cache")
			}
			return nil, mautrix.MNotFound.WithMessage("Avatar is no longer available")
		} else if err != nil {
			return nil, fmt.Errorf("failed to refresh avatar url: %w", err)
		}
		cachedInfo = avatarInfoToCacheEntry(ctx, parsedID.Avatar.TargetJID, avatar)
		err = wa.DB.AvatarCache.Put(ctx, cachedInfo)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).
				Str("avatar_id", avatar.ID).
				Msg("Failed to update avatar cache entry")
		}
	}
	return &mediaproxy.GetMediaResponseFile{
		Callback: func(w *os.File) error {
			return waClient.Client.DownloadMediaWithPathToFile(
				ctx, cachedInfo.DirectPath, nil, nil, nil, 0, "", "", w,
			)
		},
		ContentType: "", // TODO are avatars always jpeg?
	}, nil
}

func (wa *WhatsAppConnector) downloadMessageDirectMedia(ctx context.Context, parsedID *waid.ParsedMediaID, params map[string]string) (mediaproxy.GetMediaResponse, error) {
	log := zerolog.Ctx(ctx)
	msg, err := wa.Bridge.DB.Message.GetFirstPartByID(ctx, parsedID.UserLogin, parsedID.Message.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	} else if msg == nil {
		return nil, fmt.Errorf("message not found")
	}
	dmm := msg.Metadata.(*waid.MessageMetadata).DirectMediaMeta
	if dmm == nil {
		return nil, fmt.Errorf("message does not have direct media metadata")
	}
	var keys *msgconv.FailedMediaKeys
	err = json.Unmarshal(dmm, &keys)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal media keys: %w", err)
	}
	var ul *bridgev2.UserLogin
	if parsedID.UserLogin != "" {
		ul = wa.Bridge.GetCachedUserLoginByID(parsedID.UserLogin)
	} else {
		logins, err := wa.Bridge.GetUserLoginsInPortal(ctx, msg.Room)
		if err != nil {
			return nil, fmt.Errorf("failed to get user logins in portal: %w", err)
		}
		for _, login := range logins {
			if login.Client.IsLoggedIn() {
				ul = login
				break
			}
		}
	}
	if ul == nil || !ul.Client.IsLoggedIn() {
		return nil, fmt.Errorf("no logged in user found")
	}
	waClient := ul.Client.(*WhatsAppClient)
	if waClient.Client == nil {
		return nil, fmt.Errorf("no WhatsApp client found on login")
	}
	return &mediaproxy.GetMediaResponseFile{
		Callback: func(f *os.File) error {
			err := waClient.Client.DownloadToFile(ctx, keys, f)
			if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
				val := params["fi.mau.whatsapp.reload_media"]
				if val == "false" || (!wa.Config.DirectMediaAutoRequest && val != "true") {
					return ErrReloadNeeded
				}
				log.Trace().Msg("Media not found for direct download, requesting and waiting")
				err = waClient.requestAndWaitDirectMedia(ctx, msg.ID, keys)
				if err != nil {
					log.Trace().Err(err).Msg("Failed to wait for media for direct download")
					return err
				}
				log.Trace().Msg("Retrying download after successful retry")
				err = waClient.Client.DownloadToFile(ctx, keys, f)
			}
			if errors.Is(err, whatsmeow.ErrFileLengthMismatch) || errors.Is(err, whatsmeow.ErrInvalidMediaSHA256) {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Mismatching media checksums in message. Ignoring because WhatsApp seems to ignore them too")
			} else if err != nil {
				return err
			}

			if keys.MimeType == "application/was" {
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return fmt.Errorf("failed to seek to start of sticker zip: %w", err)
				} else if zipData, err := io.ReadAll(f); err != nil {
					return fmt.Errorf("failed to read sticker zip: %w", err)
				} else if data, err := msgconv.ExtractAnimatedSticker(zipData); err != nil {
					return fmt.Errorf("failed to extract animated sticker: %w %x", err, zipData)
				} else if _, err := f.WriteAt(data, 0); err != nil {
					return fmt.Errorf("failed to write animated sticker to file: %w", err)
				} else if err := f.Truncate(int64(len(data))); err != nil {
					return fmt.Errorf("failed to truncate animated sticker file: %w", err)
				}
			}

			return nil
		},
		// TODO?
		ContentType: "",
	}, nil
}

type directMediaRetry struct {
	sync.Mutex
	resultURL  string
	wait       *exsync.Event
	requested  bool
	resultType waMmsRetry.MediaRetryNotification_ResultType
}

func (wa *WhatsAppClient) getDirectMediaRetryState(msgID networkid.MessageID, create bool) *directMediaRetry {
	wa.directMediaLock.Lock()
	defer wa.directMediaLock.Unlock()
	retry, ok := wa.directMediaRetries[msgID]
	if !ok && create {
		retry = &directMediaRetry{
			wait: exsync.NewEvent(),
		}
		wa.directMediaRetries[msgID] = retry
	}
	return retry
}

func (wa *WhatsAppClient) requestAndWaitDirectMedia(ctx context.Context, rawMsgID networkid.MessageID, keys *msgconv.FailedMediaKeys) error {
	state, err := wa.requestDirectMedia(ctx, rawMsgID, keys.Key)
	if err != nil {
		return err
	}
	select {
	case <-state.wait.GetChan():
		if state.resultURL != "" {
			keys.DirectPath = state.resultURL
			return nil
		}
		switch state.resultType {
		case waMmsRetry.MediaRetryNotification_NOT_FOUND:
			return mautrix.MNotFound.WithMessage("Media not found on phone")
		default:
			return mautrix.MNotFound.WithMessage("Phone returned error response")
		}
	case <-time.After(30 * time.Second):
		return mautrix.MNotFound.WithMessage("Phone did not respond in time").WithStatus(http.StatusGatewayTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (wa *WhatsAppClient) requestDirectMedia(ctx context.Context, rawMsgID networkid.MessageID, key []byte) (*directMediaRetry, error) {
	state := wa.getDirectMediaRetryState(rawMsgID, true)
	state.Lock()
	defer state.Unlock()
	if !state.requested {
		zerolog.Ctx(ctx).Debug().Msg("Sending request for missing media in direct download")
		err := wa.sendMediaRequestDirect(rawMsgID, key)
		if err != nil {
			return nil, fmt.Errorf("failed to send media retry request: %w", err)
		}
		state.requested = true
	} else {
		zerolog.Ctx(ctx).Debug().Msg("Media retry request already sent previously, just waiting for response")
	}
	return state, nil
}

func (wa *WhatsAppClient) receiveDirectMediaRetry(ctx context.Context, msg *database.Message, retry *events.MediaRetry) {
	state := wa.getDirectMediaRetryState(msg.ID, false)
	if state != nil {
		state.Lock()
		defer func() {
			state.wait.Set()
			state.Unlock()
		}()
	}
	log := zerolog.Ctx(ctx)
	var keys msgconv.FailedMediaKeys
	err := json.Unmarshal(msg.Metadata.(*waid.MessageMetadata).DirectMediaMeta, &keys)
	if err != nil {
		log.Err(err).Msg("Failed to parse direct media metadata for media retry")
		return
	}
	retryData, err := whatsmeow.DecryptMediaRetryNotification(retry, keys.Key)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to decrypt media retry notification")
		return
	}
	if state != nil {
		state.resultType = retryData.GetResult()
	}
	if retryData.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS {
		errorName := waMmsRetry.MediaRetryNotification_ResultType_name[int32(retryData.GetResult())]
		if retryData.GetDirectPath() == "" {
			log.Warn().Str("error_name", errorName).Msg("Got error response in media retry notification")
			log.Debug().Any("error_content", retryData).Msg("Full error response content")
			return
		}
		log.Debug().Msg("Got error response in media retry notification, but response also contains a new download URL")
	}
	keys.DirectPath = retryData.GetDirectPath()
	msg.Metadata.(*waid.MessageMetadata).DirectMediaMeta, err = json.Marshal(keys)
	if err != nil {
		log.Err(err).Msg("Failed to marshal updated direct media metadata")
	} else if err = wa.Main.Bridge.DB.Message.Update(ctx, msg); err != nil {
		log.Err(err).Msg("Failed to update message with new direct media metadata")
	}
	if state != nil {
		state.resultURL = retryData.GetDirectPath()
	}
}
