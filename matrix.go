// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
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
	"maunium.net/go/gomatrix"
)

func (bridge *Bridge) HandleBotInvite(evt *gomatrix.Event) {
	intent := bridge.AppService.BotIntent()

	resp, err := intent.JoinRoom(evt.RoomID, "", nil)
	if err != nil {
		bridge.Log.Debugln("Failed to join room", evt.RoomID, "with invite from", evt.Sender)
		return
	}

	members, err := intent.JoinedMembers(resp.RoomID)
	if err != nil {
		bridge.Log.Debugln("Failed to get members in room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if len(members.Joined) < 2 {
		bridge.Log.Debugln("Leaving empty room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.LeaveRoom(resp.RoomID)
		return
	}

	hasPuppets := false
	for mxid, _ := range members.Joined {
		if mxid == intent.UserID || mxid == evt.Sender {
			continue
		} else if _, _, ok := bridge.ParsePuppetMXID(mxid); ok {
			hasPuppets = true
			continue
		}
		bridge.Log.Debugln("Leaving multi-user room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.SendNotice(resp.RoomID, "This bridge is user-specific, please don't invite me into rooms with other users.")
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if !hasPuppets {
		user := bridge.GetUser(evt.Sender)
		user.ManagementRoom = resp.RoomID
		user.Update()
		intent.SendNotice(user.ManagementRoom, "This room has been registered as your bridge management/status room.")
		bridge.Log.Debugln(resp.RoomID, "registered as a management room with", evt.Sender)
	}
}

func (bridge *Bridge) HandleMembership(evt *gomatrix.Event) {
	bridge.Log.Debugln(evt.Content, evt.Content.Membership, evt.GetStateKey())
	if evt.Content.Membership == "invite" && evt.GetStateKey() == bridge.AppService.BotMXID() {
		bridge.HandleBotInvite(evt)
	}
}

func (bridge *Bridge) HandleMessage(evt *gomatrix.Event) {
}
