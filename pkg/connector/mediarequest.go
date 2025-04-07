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
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func isFailedMedia(converted *bridgev2.ConvertedMessage) bool {
	if len(converted.Parts) == 0 || converted.Parts[0].Extra == nil {
		return false
	}
	_, ok := converted.Parts[0].Extra[msgconv.FailedMediaField].(*msgconv.PreparedMedia)
	return ok
}

func (wa *WhatsAppClient) processFailedMedia(ctx context.Context, portalKey networkid.PortalKey, msgID networkid.MessageID, converted *bridgev2.ConvertedMessage, isBackfill bool) *wadb.MediaRequest {
	if len(converted.Parts) == 0 || converted.Parts[0].Extra == nil {
		return nil
	}
	field, ok := converted.Parts[0].Extra[msgconv.FailedMediaField].(*msgconv.PreparedMedia)
	if !ok {
		return nil
	}
	req := &wadb.MediaRequest{
		UserLoginID: wa.UserLogin.ID,
		MessageID:   msgID,
		PortalKey:   portalKey,
		MediaKey:    field.FailedKeys.Key,
		Status:      wadb.MediaBackfillRequestStatusNotRequested,
	}
	if isBackfill {
		return req
	}
	err := wa.Main.DB.MediaRequest.Put(ctx, req)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to save failed media request")
	}
	if wa.Main.Config.HistorySync.MediaRequests.AutoRequestMedia && wa.Main.Config.HistorySync.MediaRequests.RequestMethod == MediaRequestMethodImmediate {
		go wa.sendMediaRequest(context.WithoutCancel(ctx), req)
	}
	return nil
}

func (wa *WhatsAppClient) mediaRequestLoop(ctx context.Context) {
	log := wa.UserLogin.Log.With().Str("loop", "media requests").Logger()
	ctx = log.WithContext(ctx)
	tzName := wa.UserLogin.Metadata.(*waid.UserLoginMetadata).Timezone
	userTz, err := time.LoadLocation(tzName)
	var startIn time.Duration
	if tzName != "" && err == nil && userTz != nil {
		now := time.Now()
		startAt := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, userTz)
		startAt = startAt.Add(time.Duration(wa.Main.Config.HistorySync.MediaRequests.RequestLocalTime) * time.Minute)
		if startAt.Before(now) {
			startAt = startAt.AddDate(0, 0, 1)
		}
		startIn = time.Until(startAt)
	} else {
		startIn = 8 * time.Hour
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(startIn):
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wa.sendMediaRequests(ctx)
		}
	}
}

func (wa *WhatsAppClient) sendMediaRequests(ctx context.Context) {
	reqs, err := wa.Main.DB.MediaRequest.GetUnrequestedForUserLogin(ctx, wa.UserLogin.ID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get media requests from database")
		return
	} else if len(reqs) == 0 {
		return
	}
	zerolog.Ctx(ctx).Info().Int("request_count", len(reqs)).Msg("Sending media requests")
	for _, req := range reqs {
		wa.sendMediaRequest(ctx, req)
	}
}

func (wa *WhatsAppClient) sendMediaRequest(ctx context.Context, req *wadb.MediaRequest) {
	log := zerolog.Ctx(ctx).With().Str("action", "send media request").Str("message_id", string(req.MessageID)).Logger()
	defer func() {
		err := wa.Main.DB.MediaRequest.Put(ctx, req)
		if err != nil {
			log.Err(err).Msg("Failed to save media request status")
		}
	}()
	msg, err := wa.Main.Bridge.DB.Message.GetPartByID(ctx, wa.UserLogin.ID, req.MessageID, "")
	if err != nil {
		log.Err(err).Msg("Failed to get media retry target message from database")
		req.Status = wadb.MediaBackfillRequestStatusRequestSkipped
		return
	} else if msg == nil {
		log.Warn().Msg("Media retry target message not found in database")
		req.Status = wadb.MediaBackfillRequestStatusRequestSkipped
		return
	} else if msg.Metadata.(*waid.MessageMetadata).Error != waid.MsgErrMediaNotFound {
		log.Debug().Msg("Not sending media retry for message that doesn't have media error")
		req.Status = wadb.MediaBackfillRequestStatusRequestSkipped
		return
	}
	err = wa.sendMediaRequestDirect(req.MessageID, req.MediaKey)
	if err != nil {
		log.Err(err).Msg("Failed to send media retry request")
		req.Status = wadb.MediaBackfillRequestStatusRequestFailed
		req.Error = err.Error()
	} else {
		log.Debug().Msg("Sent media retry request")
		req.Status = wadb.MediaBackfillRequestStatusRequested
	}
}

func (wa *WhatsAppClient) sendMediaRequestDirect(rawMsgID networkid.MessageID, key []byte) error {
	msgID, err := waid.ParseMessageID(rawMsgID)
	if err != nil {
		return fmt.Errorf("failed to parse message ID: %w", err)
	}
	return wa.Client.SendMediaRetryReceipt(&types.MessageInfo{
		ID: msgID.ID,
		MessageSource: types.MessageSource{
			IsFromMe: msgID.Sender.User == wa.JID.User,
			IsGroup:  msgID.Chat.Server != types.DefaultUserServer && msgID.Chat.Server != types.BotServer,
			Sender:   msgID.Sender,
			Chat:     msgID.Chat,
		},
	}, key)
}
