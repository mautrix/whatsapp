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

	"maunium.net/go/mautrix-whatsapp/database"
)

func (portal *Portal) MarkDisappearing(eventID id.EventID, expiresIn uint32, startNow bool) {
	if expiresIn == 0 || (!portal.bridge.Config.Bridge.DisappearingMessagesInGroups && portal.IsGroupChat()) {
		return
	}

	msg := portal.bridge.DB.DisappearingMessage.NewWithValues(portal.MXID, eventID, time.Duration(expiresIn)*time.Second, startNow)
	msg.Insert()
	if startNow {
		go portal.sleepAndDelete(msg)
	}
}

func (portal *Portal) ScheduleDisappearing() {
	if !portal.bridge.Config.Bridge.DisappearingMessagesInGroups && portal.IsGroupChat() {
		return
	}
	nowPlusHour := time.Now().Add(1 * time.Hour)
	for _, msg := range portal.bridge.DB.DisappearingMessage.StartAllUnscheduledInRoom(portal.MXID) {
		if msg.ExpireAt.Before(nowPlusHour) {
			go portal.sleepAndDelete(msg)
		}
	}
}

func (bridge *Bridge) DisappearingLoop() {
	for {
		for _, msg := range bridge.DB.DisappearingMessage.GetUpcomingScheduled(1 * time.Hour) {
			portal := bridge.GetPortalByMXID(msg.RoomID)
			go portal.sleepAndDelete(msg)
		}
		time.Sleep(1 * time.Hour)
	}
}

func (portal *Portal) sleepAndDelete(msg *database.DisappearingMessage) {
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
