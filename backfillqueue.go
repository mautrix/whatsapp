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
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

type BackfillQueue struct {
	BackfillQuery   *database.BackfillQuery
	reCheckChannels []chan bool
	log             log.Logger
}

func (bq *BackfillQueue) ReCheck() {
	bq.log.Info("Sending re-checks to %d channels", len(bq.reCheckChannels))
	for _, channel := range bq.reCheckChannels {
		go func(c chan bool) {
			c <- true
		}(channel)
	}
}

func (bq *BackfillQueue) GetNextBackfill(userID id.UserID, backfillTypes []database.BackfillType, reCheckChannel chan bool) *database.Backfill {
	for {
		if backfill := bq.BackfillQuery.GetNext(userID, backfillTypes); backfill != nil {
			backfill.MarkDispatched()
			return backfill
		} else {
			select {
			case <-reCheckChannel:
			case <-time.After(time.Minute):
			}
		}
	}
}

func (user *User) HandleBackfillRequestsLoop(backfillTypes []database.BackfillType) {
	reCheckChannel := make(chan bool)
	user.BackfillQueue.reCheckChannels = append(user.BackfillQueue.reCheckChannels, reCheckChannel)

	for {
		req := user.BackfillQueue.GetNextBackfill(user.MXID, backfillTypes, reCheckChannel)
		user.log.Infofln("Handling backfill request %s", req)

		conv := user.bridge.DB.HistorySync.GetConversation(user.MXID, req.Portal)
		if conv == nil {
			user.log.Debugfln("Could not find history sync conversation data for %s", req.Portal.String())
			req.MarkDone()
			continue
		}
		portal := user.GetPortalByJID(conv.PortalKey.JID)

		// Update the client store with basic chat settings.
		if conv.MuteEndTime.After(time.Now()) {
			user.Client.Store.ChatSettings.PutMutedUntil(conv.PortalKey.JID, conv.MuteEndTime)
		}
		if conv.Archived {
			user.Client.Store.ChatSettings.PutArchived(conv.PortalKey.JID, true)
		}
		if conv.Pinned > 0 {
			user.Client.Store.ChatSettings.PutPinned(conv.PortalKey.JID, true)
		}

		if conv.EphemeralExpiration != nil && portal.ExpirationTime != *conv.EphemeralExpiration {
			portal.ExpirationTime = *conv.EphemeralExpiration
			portal.Update(nil)
		}

		user.backfillInChunks(req, conv, portal)
		req.MarkDone()
	}
}
