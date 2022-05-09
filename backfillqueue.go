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

// RunLoop fetches backfills from the database, prioritizing immediate and forward backfills
func (bq *BackfillQueue) RunLoop(user *User) {
	for {
		if backfill := bq.BackfillQuery.GetNext(user.MXID); backfill != nil {
			if backfill.BackfillType == database.BackfillImmediate || backfill.BackfillType == database.BackfillForward {
				bq.ImmediateBackfillRequests <- backfill
			} else if backfill.BackfillType == database.BackfillDeferred {
				select {
				case <-bq.ReCheckQueue:
					// If a queue re-check is requested, interrupt sending the
					// backfill request to the deferred channel so that
					// immediate backfills can happen ASAP.
					continue
				case bq.DeferredBackfillRequests <- backfill:
				}
			} else {
				bq.log.Debugfln("Unrecognized backfill type %d in queue", backfill.BackfillType)
			}
			backfill.MarkDispatched()
		} else {
			select {
			case <-bq.ReCheckQueue:
			case <-time.After(time.Minute):
			}
		}
	}
}
