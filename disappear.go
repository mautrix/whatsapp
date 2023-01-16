// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"maunium.net/go/mautrix-whatsapp/database"
)

func (portal *Portal) MarkDisappearing(txn dbutil.Execable, eventID id.EventID, expiresIn time.Duration, startsAt time.Time) {
	if expiresIn == 0 {
		return
	}
	expiresAt := startsAt.Add(expiresIn)

	msg := portal.bridge.DB.DisappearingMessage.NewWithValues(portal.MXID, eventID, expiresIn, expiresAt)
	msg.Insert(txn)
	if expiresAt.Before(time.Now().Add(1 * time.Hour)) {
		go portal.sleepAndDelete(msg)
	}
}

func (br *WABridge) SleepAndDeleteUpcoming() {
	for _, msg := range br.DB.DisappearingMessage.GetUpcomingScheduled(1 * time.Hour) {
		portal := br.GetPortalByMXID(msg.RoomID)
		if portal == nil {
			msg.Delete()
		} else {
			go portal.sleepAndDelete(msg)
		}
	}
}

func (portal *Portal) sleepAndDelete(msg *database.DisappearingMessage) {
	if _, alreadySleeping := portal.currentlySleepingToDelete.LoadOrStore(msg.EventID, true); alreadySleeping {
		return
	}
	defer portal.currentlySleepingToDelete.Delete(msg.EventID)

	sleepTime := msg.ExpireAt.Sub(time.Now())
	portal.log.Debugfln("Sleeping for %s to make %s disappear", sleepTime, msg.EventID)
	time.Sleep(sleepTime)
	_, err := portal.MainIntent().RedactEvent(msg.RoomID, msg.EventID, mautrix.ReqRedact{
		Reason: "Message expired",
		TxnID:  fmt.Sprintf("mxwa_disappear_%s", msg.EventID),
	})
	if err != nil {
		portal.log.Warnfln("Failed to make %s disappear: %v", msg.EventID, err)
	} else {
		portal.log.Debugfln("Disappeared %s", msg.EventID)
	}
	msg.Delete()
}
