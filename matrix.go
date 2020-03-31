// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2019 Tulir Asokan
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
	"strings"

	"maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix/format"

	"maunium.net/go/mautrix-whatsapp/types"
)

type MatrixHandler struct {
	bridge *Bridge
	as     *appservice.AppService
	log    maulogger.Logger
	cmd    *CommandHandler
}

func NewMatrixHandler(bridge *Bridge) *MatrixHandler {
	handler := &MatrixHandler{
		bridge: bridge,
		as:     bridge.AS,
		log:    bridge.Log.Sub("Matrix"),
		cmd:    NewCommandHandler(bridge),
	}
	bridge.EventProcessor.On(mautrix.EventMessage, handler.HandleMessage)
	bridge.EventProcessor.On(mautrix.EventSticker, handler.HandleMessage)
	bridge.EventProcessor.On(mautrix.EventRedaction, handler.HandleRedaction)
	bridge.EventProcessor.On(mautrix.StateMember, handler.HandleMembership)
	bridge.EventProcessor.On(mautrix.StateRoomName, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(mautrix.StateRoomAvatar, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(mautrix.StateTopic, handler.HandleRoomMetadata)
	return handler
}

func (mx *MatrixHandler) HandleBotInvite(evt *mautrix.Event) {
	intent := mx.as.BotIntent()

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}

	resp, err := intent.JoinRoom(evt.RoomID, "", nil)
	if err != nil {
		mx.log.Debugln("Failed to join room", evt.RoomID, "with invite from", evt.Sender)
		return
	}

	members, err := intent.JoinedMembers(resp.RoomID)
	if err != nil {
		mx.log.Debugln("Failed to get members in room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if len(members.Joined) < 2 {
		mx.log.Debugln("Leaving empty room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if !user.Whitelisted {
		intent.SendNotice(resp.RoomID, "You are not whitelisted to use this bridge.\n"+
			"If you're the owner of this bridge, see the bridge.permissions section in your config file.")
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if evt.RoomID == mx.bridge.Config.Bridge.Relaybot.ManagementRoom {
		intent.SendNotice(evt.RoomID, "This is the relaybot management room. Send `!wa help` to get a list of commands.")
		mx.log.Debugln("Joined relaybot management room", evt.RoomID, "after invite from", evt.Sender)
		return
	}

	hasPuppets := false
	for mxid, _ := range members.Joined {
		if mxid == intent.UserID || mxid == evt.Sender {
			continue
		} else if _, ok := mx.bridge.ParsePuppetMXID(types.MatrixUserID(mxid)); ok {
			hasPuppets = true
			continue
		}
		mx.log.Debugln("Leaving multi-user room", resp.RoomID, "after accepting invite from", evt.Sender)
		intent.SendNotice(resp.RoomID, "This bridge is user-specific, please don't invite me into rooms with other users.")
		intent.LeaveRoom(resp.RoomID)
		return
	}

	if !hasPuppets {
		user := mx.bridge.GetUserByMXID(types.MatrixUserID(evt.Sender))
		user.SetManagementRoom(types.MatrixRoomID(resp.RoomID))
		intent.SendNotice(string(user.ManagementRoom), "This room has been registered as your bridge management/status room. Send `help` to get a list of commands.")
		mx.log.Debugln(resp.RoomID, "registered as a management room with", evt.Sender)
	}
}

func (mx *MatrixHandler) HandleMembership(evt *mautrix.Event) {
	if evt.Content.Membership == "invite" && evt.GetStateKey() == mx.as.BotMXID() {
		mx.HandleBotInvite(evt)
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		return
	}

	user := mx.bridge.GetUserByMXID(types.MatrixUserID(evt.Sender))
	if user == nil || !user.Whitelisted || !user.IsConnected() {
		return
	}

	if evt.Content.Membership == "leave" {
		if evt.GetStateKey() == evt.Sender {
			if portal.IsPrivateChat() || evt.Unsigned.PrevContent.Membership == "join" {
				portal.HandleMatrixLeave(user)
			}
		} else {
			portal.HandleMatrixKick(user, evt)
		}
	}
}

func (mx *MatrixHandler) HandleRoomMetadata(evt *mautrix.Event) {
	user := mx.bridge.GetUserByMXID(types.MatrixUserID(evt.Sender))
	if user == nil || !user.Whitelisted || !user.IsConnected() {
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil || portal.IsPrivateChat() {
		return
	}

	var resp <-chan string
	var err error
	switch evt.Type {
	case mautrix.StateRoomName:
		resp, err = user.Conn.UpdateGroupSubject(evt.Content.Name, portal.Key.JID)
	case mautrix.StateTopic:
		resp, err = user.Conn.UpdateGroupDescription(portal.Key.JID, evt.Content.Topic)
	case mautrix.StateRoomAvatar:
		return
	}
	if err != nil {
		mx.log.Errorln(err)
	} else {
		out := <-resp
		mx.log.Infoln(out)
	}
}

func (mx *MatrixHandler) HandleMessage(evt *mautrix.Event) {
	if _, isPuppet := mx.bridge.ParsePuppetMXID(evt.Sender); evt.Sender == mx.bridge.Bot.UserID || isPuppet {
		return
	}
	isCustomPuppet, ok := evt.Content.Raw["net.maunium.whatsapp.puppet"].(bool)
	if ok && isCustomPuppet && mx.bridge.GetPuppetByCustomMXID(evt.Sender) != nil {
		return
	}

	roomID := types.MatrixRoomID(evt.RoomID)
	user := mx.bridge.GetUserByMXID(types.MatrixUserID(evt.Sender))

	if !user.RelaybotWhitelisted {
		return
	}

	if user.Whitelisted && evt.Content.MsgType == mautrix.MsgText {
		commandPrefix := mx.bridge.Config.Bridge.CommandPrefix
		hasCommandPrefix := strings.HasPrefix(evt.Content.Body, commandPrefix)
		if hasCommandPrefix {
			evt.Content.Body = strings.TrimLeft(evt.Content.Body[len(commandPrefix):], " ")
		}
		if hasCommandPrefix || roomID == user.ManagementRoom {
			mx.cmd.Handle(roomID, user, evt.Content.Body)
			return
		}
	}

	portal := mx.bridge.GetPortalByMXID(roomID)
	if portal != nil && (user.Whitelisted || portal.HasRelaybot()) {
		portal.HandleMatrixMessage(user, evt)
	}
}

func (mx *MatrixHandler) HandleRedaction(evt *mautrix.Event) {
	if _, isPuppet := mx.bridge.ParsePuppetMXID(evt.Sender); evt.Sender == mx.bridge.Bot.UserID || isPuppet {
		return
	}

	roomID := types.MatrixRoomID(evt.RoomID)
	user := mx.bridge.GetUserByMXID(types.MatrixUserID(evt.Sender))

	if !user.Whitelisted {
		return
	}

	if !user.HasSession() {
		return
	} else if !user.IsConnected() {
		msg := format.RenderMarkdown(fmt.Sprintf("[%[1]s](https://matrix.to/#/%[1]s): \u26a0 "+
			"You are not connected to WhatsApp, so your redaction was not bridged. "+
			"Use `%[2]s reconnect` to reconnect.", user.MXID, mx.bridge.Config.Bridge.CommandPrefix))
		msg.MsgType = mautrix.MsgNotice
		_, _ = mx.bridge.Bot.SendMessageEvent(roomID, mautrix.EventMessage, msg)
		return
	}

	portal := mx.bridge.GetPortalByMXID(roomID)
	if portal != nil {
		portal.HandleMatrixRedaction(user, evt)
	}
}
