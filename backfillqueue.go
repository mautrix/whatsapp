// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan, Sumner Evans
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

package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

type BackfillQueue struct {
	BackfillQuery   *database.BackfillTaskQuery
	reCheckChannels []chan bool
}

func (bq *BackfillQueue) ReCheck() {
	for _, channel := range bq.reCheckChannels {
		go func(c chan bool) {
			c <- true
		}(channel)
	}
}

func (bq *BackfillQueue) GetNextBackfill(ctx context.Context, userID id.UserID, backfillTypes []database.BackfillType, waitForBackfillTypes []database.BackfillType, reCheckChannel chan bool) *database.BackfillTask {
	for {
		if !bq.BackfillQuery.HasUnstartedOrInFlightOfType(ctx, userID, waitForBackfillTypes) {
			// check for immediate when dealing with deferred
			if backfill, err := bq.BackfillQuery.GetNext(ctx, userID, backfillTypes); err != nil {
				zerolog.Ctx(ctx).Err(err).Msg("Failed to get next backfill task")
			} else if backfill != nil {
				err = backfill.MarkDispatched(ctx)
				if err != nil {
					zerolog.Ctx(ctx).Warn().Err(err).
						Int("queue_id", backfill.QueueID).
						Msg("Failed to mark backfill task as dispatched")
				}
				return backfill
			}
		}

		select {
		case <-reCheckChannel:
		case <-time.After(time.Minute):
		}
	}
}

func (user *User) HandleBackfillRequestsLoop(backfillTypes []database.BackfillType, waitForBackfillTypes []database.BackfillType) {
	log := user.zlog.With().
		Str("action", "backfill request loop").
		Any("types", backfillTypes).
		Logger()
	ctx := log.WithContext(context.TODO())
	reCheckChannel := make(chan bool)
	user.BackfillQueue.reCheckChannels = append(user.BackfillQueue.reCheckChannels, reCheckChannel)

	for {
		req := user.BackfillQueue.GetNextBackfill(ctx, user.MXID, backfillTypes, waitForBackfillTypes, reCheckChannel)
		log.Info().Any("backfill_request", req).Msg("Handling backfill request")
		log := log.With().
			Int("queue_id", req.QueueID).
			Stringer("portal_jid", req.Portal.JID).
			Logger()
		ctx := log.WithContext(ctx)

		conv, err := user.bridge.DB.HistorySync.GetConversation(ctx, user.MXID, req.Portal)
		if err != nil {
			log.Err(err).Msg("Failed to get conversation data for backfill request")
			continue
		} else if conv == nil {
			log.Debug().Msg("Couldn't find conversation data for backfill request")
			err = req.MarkDone(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to mark backfill request as done after data was not found")
			}
			continue
		}
		portal := user.GetPortalByJID(conv.PortalKey.JID)

		// Update the client store with basic chat settings.
		if conv.MuteEndTime.After(time.Now()) {
			err = user.Client.Store.ChatSettings.PutMutedUntil(conv.PortalKey.JID, conv.MuteEndTime)
			if err != nil {
				log.Err(err).Msg("Failed to save muted until time from conversation data")
			}
		}
		if conv.Archived {
			err = user.Client.Store.ChatSettings.PutArchived(conv.PortalKey.JID, true)
			if err != nil {
				log.Err(err).Msg("Failed to save archived state from conversation data")
			}
		}
		if conv.Pinned > 0 {
			err = user.Client.Store.ChatSettings.PutPinned(conv.PortalKey.JID, true)
			if err != nil {
				log.Err(err).Msg("Failed to save pinned state from conversation data")
			}
		}

		if conv.EphemeralExpiration != nil && portal.ExpirationTime != *conv.EphemeralExpiration {
			log.Debug().
				Uint32("old_time", portal.ExpirationTime).
				Uint32("new_time", *conv.EphemeralExpiration).
				Msg("Updating portal ephemeral expiration time")
			portal.ExpirationTime = *conv.EphemeralExpiration
			err = portal.Update(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to save portal after updating expiration time")
			}
		}

		user.backfillInChunks(ctx, req, conv, portal)
		err = req.MarkDone(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to mark backfill request as done after backfilling")
		}
	}
}
