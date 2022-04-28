// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan, Sumner Evans
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
	"time"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix-whatsapp/database"
)

type BackfillQueue struct {
	BackfillQuery             *database.BackfillQuery
	ImmediateBackfillRequests chan *database.Backfill
	DeferredBackfillRequests  chan *database.Backfill
	ReCheckQueue              chan bool

	log log.Logger
}

// Immediate backfills should happen first, then deferred backfills and lastly
// media backfills.
func (bq *BackfillQueue) RunLoop(user *User) {
	for {
		if immediate := bq.BackfillQuery.GetNext(user.MXID, database.BackfillImmediate); immediate != nil {
			bq.ImmediateBackfillRequests <- immediate
			immediate.MarkDone()
		} else if backfill := bq.BackfillQuery.GetNext(user.MXID, database.BackfillDeferred); backfill != nil {
			bq.DeferredBackfillRequests <- backfill
			backfill.MarkDone()
		} else if mediaBackfill := bq.BackfillQuery.GetNext(user.MXID, database.BackfillMedia); mediaBackfill != nil {
			bq.DeferredBackfillRequests <- mediaBackfill
			mediaBackfill.MarkDone()
		} else {
			select {
			case <-bq.ReCheckQueue:
			case <-time.After(time.Minute):
			}
		}
	}
}
