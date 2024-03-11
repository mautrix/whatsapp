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

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

func (portal *Portal) MarkDisappearing(ctx context.Context, eventID id.EventID, expiresIn time.Duration, startsAt time.Time) {
	if expiresIn == 0 {
		return
	}
	expiresAt := startsAt.Add(expiresIn)

	msg := portal.bridge.DB.DisappearingMessage.NewWithValues(portal.MXID, eventID, expiresIn, expiresAt)
	err := msg.Insert(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to insert disappearing message")
	}
	if expiresAt.Before(time.Now().Add(1 * time.Hour)) {
		go portal.sleepAndDelete(context.WithoutCancel(ctx), msg)
	}
}

func (br *WABridge) SleepAndDeleteUpcoming(ctx context.Context) {
	msgs, err := br.DB.DisappearingMessage.GetUpcomingScheduled(ctx, 1*time.Hour)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get upcoming disappearing messages")
		return
	}
	for _, msg := range msgs {
		portal := br.GetPortalByMXID(msg.RoomID)
		if portal == nil {
			err = msg.Delete(ctx)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).
					Stringer("event_id", msg.EventID).
					Msg("Failed to delete disappearing message row with no portal")
			}
		} else {
			go portal.sleepAndDelete(ctx, msg)
		}
	}
}

func (portal *Portal) sleepAndDelete(ctx context.Context, msg *database.DisappearingMessage) {
	if _, alreadySleeping := portal.currentlySleepingToDelete.LoadOrStore(msg.EventID, true); alreadySleeping {
		return
	}
	defer portal.currentlySleepingToDelete.Delete(msg.EventID)
	log := zerolog.Ctx(ctx)

	sleepTime := msg.ExpireAt.Sub(time.Now())
	log.Debug().
		Stringer("room_id", portal.MXID).
		Stringer("event_id", msg.EventID).
		Dur("sleep_time", sleepTime).
		Msg("Sleeping before making message disappear")
	time.Sleep(sleepTime)
	_, err := portal.MainIntent().RedactEvent(ctx, msg.RoomID, msg.EventID, mautrix.ReqRedact{
		Reason: "Message expired",
		TxnID:  fmt.Sprintf("mxwa_disappear_%s", msg.EventID),
	})
	if err != nil {
		log.Err(err).
			Stringer("room_id", portal.MXID).
			Stringer("event_id", msg.EventID).
			Msg("Failed to make event disappear")
	} else {
		log.Debug().
			Stringer("room_id", portal.MXID).
			Stringer("event_id", msg.EventID).
			Msg("Disappeared event")
	}
	err = msg.Delete(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to delete disapperaing message row in database after redacting event")
	}
}
