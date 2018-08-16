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
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/gomatrix"
)

type MatrixListener struct {
	bridge *Bridge
	as     *appservice.AppService
	log    *log.Sublogger
	stop   chan struct{}
}

func NewMatrixListener(bridge *Bridge) *MatrixListener {
	return &MatrixListener{
		bridge: bridge,
		as:     bridge.AppService,
		stop:   make(chan struct{}, 1),
		log:    bridge.Log.CreateSublogger("Matrix", log.LevelDebug),
	}
}

func (ml *MatrixListener) Start() {
	for {
		select {
		case evt := <-ml.bridge.AppService.Events:
			log.Debugln("Received Matrix event:", evt)
			switch evt.Type {
			case gomatrix.StateMember:
				ml.HandleMembership(evt)
			case gomatrix.EventMessage:
				ml.HandleMessage(evt)
			}
		case <-ml.stop:
			return
		}
	}
}

func (ml *MatrixListener) HandleBotInvite(evt *gomatrix.Event) {
	cli := ml.as.BotClient()

	resp, err := cli.JoinRoom(evt.RoomID, "", nil)
	if err != nil {
		ml.log.Debugln("Failed to join room", evt.RoomID, "with invite from", evt.Sender)
		return
	}

	members, err := cli.JoinedMembers(resp.RoomID)
	if err != nil {
		ml.log.Debugln("Failed to get members in room", resp.RoomID, "after accepting invite from", evt.Sender)
		cli.LeaveRoom(resp.RoomID)
		return
	}

	if len(members.Joined) < 2 {
		ml.log.Debugln("Leaving empty room", resp.RoomID, "after accepting invite from", evt.Sender)
		cli.LeaveRoom(resp.RoomID)
		return
	}
	for mxid, _ := range members.Joined {
		if mxid == cli.UserID || mxid == evt.Sender {
			continue
		} else if true { // TODO check if mxid is WhatsApp puppet

			continue
		}
		ml.log.Debugln("Leaving multi-user room", resp.RoomID, "after accepting invite from", evt.Sender)
		cli.SendNotice(resp.RoomID, "This bridge is user-specific, please don't invite me into rooms with other users.")
		cli.LeaveRoom(resp.RoomID)
		return
	}

	user := ml.bridge.GetUser(evt.Sender)
	user.ManagementRoom = resp.RoomID
	user.Update()
	cli.SendNotice(user.ManagementRoom, "This room has been registered as your bridge management/status room.")
	ml.log.Debugln(resp.RoomID, "registered as a management room with", evt.Sender)
}

func (ml *MatrixListener) HandleMembership(evt *gomatrix.Event) {
	if evt.Content.Membership == "invite" && evt.GetStateKey() == ml.as.BotMXID() {
		ml.HandleBotInvite(evt)
	}
}

func (ml *MatrixListener) HandleMessage(evt *gomatrix.Event) {

}

func (ml *MatrixListener) Stop() {
	ml.stop <- struct{}{}
}
