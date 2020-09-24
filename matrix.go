// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
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
	bridge.EventProcessor.On(event.EventMessage, handler.HandleMessage)
	bridge.EventProcessor.On(event.EventEncrypted, handler.HandleEncrypted)
	bridge.EventProcessor.On(event.EventSticker, handler.HandleMessage)
	bridge.EventProcessor.On(event.EventRedaction, handler.HandleRedaction)
	bridge.EventProcessor.On(event.StateMember, handler.HandleMembership)
	bridge.EventProcessor.On(event.StateRoomName, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(event.StateRoomAvatar, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(event.StateTopic, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(event.StateEncryption, handler.HandleEncryption)
	return handler
}

func (mx *MatrixHandler) HandleEncryption(evt *event.Event) {
	defer mx.bridge.Metrics.TrackEvent(evt.Type)()
	if evt.Content.AsEncryption().Algorithm != id.AlgorithmMegolmV1 {
		return
	}
	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil && !portal.Encrypted {
		mx.log.Debugfln("%s enabled encryption in %s", evt.Sender, evt.RoomID)
		portal.Encrypted = true
		portal.Update()
	}
}

func (mx *MatrixHandler) joinAndCheckMembers(evt *event.Event, intent *appservice.IntentAPI) *mautrix.RespJoinedMembers {
	resp, err := intent.JoinRoomByID(evt.RoomID)
	if err != nil {
		mx.log.Debugfln("Failed to join room %s as %s with invite from %s: %v", evt.RoomID, intent.UserID, evt.Sender, err)
		return nil
	}

	members, err := intent.JoinedMembers(resp.RoomID)
	if err != nil {
		mx.log.Debugfln("Failed to get members in room %s after accepting invite from %s as %s: %v", resp.RoomID, evt.Sender, intent.UserID, err)
		_, _ = intent.LeaveRoom(resp.RoomID)
		return nil
	}

	if len(members.Joined) < 2 {
		mx.log.Debugln("Leaving empty room", resp.RoomID, "after accepting invite from", evt.Sender, "as", intent.UserID)
		_, _ = intent.LeaveRoom(resp.RoomID)
		return nil
	}
	return members
}

func (mx *MatrixHandler) HandleBotInvite(evt *event.Event) {
	intent := mx.as.BotIntent()

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}

	members := mx.joinAndCheckMembers(evt, intent)
	if members == nil {
		return
	}

	if !user.Whitelisted {
		_, _ = intent.SendNotice(evt.RoomID, "You are not whitelisted to use this bridge.\n"+
			"If you're the owner of this bridge, see the bridge.permissions section in your config file.")
		_, _ = intent.LeaveRoom(evt.RoomID)
		return
	}

	if evt.RoomID == mx.bridge.Config.Bridge.Relaybot.ManagementRoom {
		_, _ = intent.SendNotice(evt.RoomID, "This is the relaybot management room. Send `!wa help` to get a list of commands.")
		mx.log.Debugln("Joined relaybot management room", evt.RoomID, "after invite from", evt.Sender)
		return
	}

	hasPuppets := false
	for mxid, _ := range members.Joined {
		if mxid == intent.UserID || mxid == evt.Sender {
			continue
		} else if _, ok := mx.bridge.ParsePuppetMXID(mxid); ok {
			hasPuppets = true
			continue
		}
		mx.log.Debugln("Leaving multi-user room", evt.RoomID, "after accepting invite from", evt.Sender)
		_, _ = intent.SendNotice(evt.RoomID, "This bridge is user-specific, please don't invite me into rooms with other users.")
		_, _ = intent.LeaveRoom(evt.RoomID)
		return
	}

	if !hasPuppets && (len(user.ManagementRoom) == 0 || evt.Content.AsMember().IsDirect) {
		user.SetManagementRoom(evt.RoomID)
		_, _ = intent.SendNotice(user.ManagementRoom, "This room has been registered as your bridge management/status room. Send `help` to get a list of commands.")
		mx.log.Debugln(evt.RoomID, "registered as a management room with", evt.Sender)
	}
}

func (mx *MatrixHandler) handlePrivatePortal(roomID id.RoomID, inviter *User, puppet *Puppet, key database.PortalKey) {
	portal := mx.bridge.GetPortalByJID(key)

	if len(portal.MXID) == 0 {
		mx.createPrivatePortalFromInvite(roomID, inviter, puppet, portal)
		return
	}

	err := portal.MainIntent().EnsureInvited(portal.MXID, inviter.MXID)
	if err != nil {
		mx.log.Warnfln("Failed to invite %s to existing private chat portal %s with %s: %v. Redirecting portal to new room...", inviter.MXID, portal.MXID, puppet.JID, err)
		mx.createPrivatePortalFromInvite(roomID, inviter, puppet, portal)
		return
	}
	intent := puppet.DefaultIntent()
	_, _ = intent.SendNotice(roomID, "You already have a private chat portal with me at %s")
	mx.log.Debugln("Leaving private chat room", roomID, "as", puppet.MXID, "after accepting invite from", inviter.MXID, "as we already have chat with the user")
	_, _ = intent.LeaveRoom(roomID)
}

func (mx *MatrixHandler) createPrivatePortalFromInvite(roomID id.RoomID, inviter *User, puppet *Puppet, portal *Portal) {
	portal.MXID = roomID
	portal.Topic = "WhatsApp private chat"
	_, _ = portal.MainIntent().SetRoomTopic(portal.MXID, portal.Topic)
	if portal.bridge.Config.Bridge.PrivateChatPortalMeta {
		portal.Name = puppet.Displayname
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		_, _ = portal.MainIntent().SetRoomName(portal.MXID, portal.Name)
		_, _ = portal.MainIntent().SetRoomAvatar(portal.MXID, portal.AvatarURL)
	} else {
		portal.Name = ""
	}
	portal.log.Infoln("Created private chat portal in %s after invite from", roomID, inviter.MXID)
	intent := puppet.DefaultIntent()

	if mx.bridge.Config.Bridge.Encryption.Default {
		_, err := intent.InviteUser(roomID, &mautrix.ReqInviteUser{UserID: mx.bridge.Bot.UserID})
		if err != nil {
			portal.log.Warnln("Failed to invite bridge bot to enable e2be:", err)
		}
		err = mx.bridge.Bot.EnsureJoined(roomID)
		if err != nil {
			portal.log.Warnln("Failed to join as bridge bot to enable e2be:", err)
		}
		_, err = intent.SendStateEvent(roomID, event.StateEncryption, "", &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1})
		if err != nil {
			portal.log.Warnln("Failed to enable e2be:", err)
		}
		mx.as.StateStore.SetMembership(roomID, inviter.MXID, event.MembershipJoin)
		mx.as.StateStore.SetMembership(roomID, puppet.MXID, event.MembershipJoin)
		mx.as.StateStore.SetMembership(roomID, mx.bridge.Bot.UserID, event.MembershipJoin)
		portal.Encrypted = true
	}
	portal.Update()
	portal.UpdateBridgeInfo()
	_, _ = intent.SendNotice(roomID, "Private chat portal created")

	err := portal.FillInitialHistory(inviter)
	if err != nil {
		portal.log.Errorln("Failed to fill history:", err)
	}

	inviter.addPortalToCommunity(portal)
	inviter.addPuppetToCommunity(puppet)
}

func (mx *MatrixHandler) HandlePuppetInvite(evt *event.Event, inviter *User, puppet *Puppet) {
	intent := puppet.DefaultIntent()
	members := mx.joinAndCheckMembers(evt, intent)
	if members == nil {
		return
	}
	var hasBridgeBot, hasOtherUsers bool
	for mxid, _ := range members.Joined {
		if mxid == intent.UserID || mxid == inviter.MXID {
			continue
		} else if mxid == mx.bridge.Bot.UserID {
			hasBridgeBot = true
		} else {
			hasOtherUsers = true
		}
	}
	if !hasBridgeBot && !hasOtherUsers {
		key := database.NewPortalKey(puppet.JID, inviter.JID)
		mx.handlePrivatePortal(evt.RoomID, inviter, puppet, key)
	} else if !hasBridgeBot {
		mx.log.Debugln("Leaving multi-user room", evt.RoomID, "as", puppet.MXID, "after accepting invite from", evt.Sender)
		_, _ = intent.SendNotice(evt.RoomID, "Please invite the bridge bot first if you want to bridge to a WhatsApp group.")
		_, _ = intent.LeaveRoom(evt.RoomID)
	} else {
		_, _ = intent.SendNotice(evt.RoomID, "This puppet will remain inactive until this room is bridged to a WhatsApp group.")
	}
}

func (mx *MatrixHandler) HandleMembership(evt *event.Event) {
	if _, isPuppet := mx.bridge.ParsePuppetMXID(evt.Sender); evt.Sender == mx.bridge.Bot.UserID || isPuppet {
		return
	}
	defer mx.bridge.Metrics.TrackEvent(evt.Type)()

	if mx.bridge.Crypto != nil {
		mx.bridge.Crypto.HandleMemberEvent(evt)
	}

	content := evt.Content.AsMember()
	if content.Membership == event.MembershipInvite && id.UserID(evt.GetStateKey()) == mx.as.BotMXID() {
		mx.HandleBotInvite(evt)
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil || !user.Whitelisted || !user.IsConnected() {
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		puppet := mx.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
		if content.Membership == event.MembershipInvite && puppet != nil {
			mx.HandlePuppetInvite(evt, user, puppet)
		}
		return
	}

	isSelf := id.UserID(evt.GetStateKey()) == evt.Sender

	if content.Membership == event.MembershipLeave {
		if isSelf {
			if evt.Unsigned.PrevContent != nil {
				_ = evt.Unsigned.PrevContent.ParseRaw(evt.Type)
				prevContent, ok := evt.Unsigned.PrevContent.Parsed.(*event.MemberEventContent)
				if ok {
					if portal.IsPrivateChat() || prevContent.Membership == "join" {
						portal.HandleMatrixLeave(user)
					}
				}
			}
		} else {
			portal.HandleMatrixKick(user, evt)
		}
	} else if content.Membership == event.MembershipInvite && !isSelf {
		portal.HandleMatrixInvite(user, evt)
	}
}

func (mx *MatrixHandler) HandleRoomMetadata(evt *event.Event) {
	defer mx.bridge.Metrics.TrackEvent(evt.Type)()
	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil || !user.Whitelisted || !user.IsConnected() {
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil || portal.IsPrivateChat() {
		return
	}

	var resp <-chan string
	var err error
	switch content := evt.Content.Parsed.(type) {
	case *event.RoomNameEventContent:
		resp, err = user.Conn.UpdateGroupSubject(content.Name, portal.Key.JID)
	case *event.TopicEventContent:
		resp, err = user.Conn.UpdateGroupDescription(portal.Key.JID, content.Topic)
	case *event.RoomAvatarEventContent:
		return
	}
	if err != nil {
		mx.log.Errorln(err)
	} else {
		out := <-resp
		mx.log.Infoln(out)
	}
}

func (mx *MatrixHandler) shouldIgnoreEvent(evt *event.Event) bool {
	if _, isPuppet := mx.bridge.ParsePuppetMXID(evt.Sender); evt.Sender == mx.bridge.Bot.UserID || isPuppet {
		return true
	}
	isCustomPuppet, ok := evt.Content.Raw["net.maunium.whatsapp.puppet"].(bool)
	if ok && isCustomPuppet && mx.bridge.GetPuppetByCustomMXID(evt.Sender) != nil {
		return true
	}
	user := mx.bridge.GetUserByMXID(evt.Sender)
	if !user.RelaybotWhitelisted {
		return true
	}
	return false
}

const sessionWaitTimeout = 3 * time.Second

func (mx *MatrixHandler) HandleEncrypted(evt *event.Event) {
	defer mx.bridge.Metrics.TrackEvent(evt.Type)()
	if mx.shouldIgnoreEvent(evt) || mx.bridge.Crypto == nil {
		return
	}

	decrypted, err := mx.bridge.Crypto.Decrypt(evt)
	if err != nil {
		content := evt.Content.AsEncrypted()
		if errors.Is(err, NoSessionFound) && mx.bridge.Crypto.WaitForSession(evt.RoomID, content.SenderKey, content.SessionID, sessionWaitTimeout) {
			mx.log.Debugfln("Got session %s to decrypt %s after waiting a while, trying to decrypt again", evt.ID, content.SessionID)
			decrypted, err = mx.bridge.Crypto.Decrypt(evt)
		}
	}
	if err != nil {
		mx.log.Warnfln("Failed to decrypt %s: %v", evt.ID, err)
		_, _ = mx.bridge.Bot.SendNotice(evt.RoomID, fmt.Sprintf(
			"\u26a0 Your message was not bridged: %v. "+
				"Try restarting your client if this error keeps happening.", err))
		return
	}
	mx.bridge.EventProcessor.Dispatch(decrypted)
}

func (mx *MatrixHandler) HandleMessage(evt *event.Event) {
	defer mx.bridge.Metrics.TrackEvent(evt.Type)()
	if mx.shouldIgnoreEvent(evt) {
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)
	content := evt.Content.AsMessage()
	if user.Whitelisted && content.MsgType == event.MsgText {
		commandPrefix := mx.bridge.Config.Bridge.CommandPrefix
		hasCommandPrefix := strings.HasPrefix(content.Body, commandPrefix)
		if hasCommandPrefix {
			content.Body = strings.TrimLeft(content.Body[len(commandPrefix):], " ")
		}
		if hasCommandPrefix || evt.RoomID == user.ManagementRoom {
			mx.cmd.Handle(evt.RoomID, user, content.Body)
			return
		}
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil && (user.Whitelisted || portal.HasRelaybot()) {
		portal.HandleMatrixMessage(user, evt)
	}
}

func (mx *MatrixHandler) HandleRedaction(evt *event.Event) {
	defer mx.bridge.Metrics.TrackEvent(evt.Type)()
	if _, isPuppet := mx.bridge.ParsePuppetMXID(evt.Sender); evt.Sender == mx.bridge.Bot.UserID || isPuppet {
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)

	if !user.Whitelisted {
		return
	}

	if !user.HasSession() {
		return
	} else if !user.IsConnected() {
		msg := format.RenderMarkdown(fmt.Sprintf("[%[1]s](https://matrix.to/#/%[1]s): \u26a0 "+
			"You are not connected to WhatsApp, so your redaction was not bridged. "+
			"Use `%[2]s reconnect` to reconnect.", user.MXID, mx.bridge.Config.Bridge.CommandPrefix), true, false)
		msg.MsgType = event.MsgNotice
		_, _ = mx.bridge.Bot.SendMessageEvent(evt.RoomID, event.EventMessage, msg)
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil {
		portal.HandleMatrixRedaction(user, evt)
	}
}
