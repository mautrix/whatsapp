// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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

	"go.mau.fi/whatsmeow/types"

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
	bridge.EventProcessor.On(event.EventReaction, handler.HandleReaction)
	bridge.EventProcessor.On(event.EventRedaction, handler.HandleRedaction)
	bridge.EventProcessor.On(event.StateMember, handler.HandleMembership)
	bridge.EventProcessor.On(event.StateRoomName, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(event.StateRoomAvatar, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(event.StateTopic, handler.HandleRoomMetadata)
	bridge.EventProcessor.On(event.StateEncryption, handler.HandleEncryption)
	bridge.EventProcessor.On(event.EphemeralEventPresence, handler.HandlePresence)
	bridge.EventProcessor.On(event.EphemeralEventReceipt, handler.HandleReceipt)
	bridge.EventProcessor.On(event.EphemeralEventTyping, handler.HandleTyping)
	return handler
}

func (mx *MatrixHandler) HandleEncryption(evt *event.Event) {
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()
	if evt.Content.AsEncryption().Algorithm != id.AlgorithmMegolmV1 {
		return
	}
	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil && !portal.Encrypted {
		mx.log.Debugfln("%s enabled encryption in %s", evt.Sender, evt.RoomID)
		portal.Encrypted = true
		portal.Update(nil)
		if portal.IsPrivateChat() {
			err := mx.as.BotIntent().EnsureJoined(portal.MXID, appservice.EnsureJoinedParams{BotOverride: portal.MainIntent().Client})
			if err != nil {
				mx.log.Errorfln("Failed to join bot to %s after encryption was enabled: %v", evt.RoomID, err)
			}
		}
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

func (mx *MatrixHandler) sendNoticeWithMarkdown(roomID id.RoomID, message string) (*mautrix.RespSendEvent, error) {
	intent := mx.as.BotIntent()
	content := format.RenderMarkdown(message, true, false)
	content.MsgType = event.MsgNotice
	return intent.SendMessageEvent(roomID, event.EventMessage, content)
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

	_, _ = mx.sendNoticeWithMarkdown(evt.RoomID, mx.bridge.Config.Bridge.ManagementRoomText.Welcome)

	if len(members.Joined) == 2 && (len(user.ManagementRoom) == 0 || evt.Content.AsMember().IsDirect) {
		user.SetManagementRoom(evt.RoomID)
		_, _ = intent.SendNotice(user.ManagementRoom, "This room has been registered as your bridge management/status room.")
		mx.log.Debugln(evt.RoomID, "registered as a management room with", evt.Sender)
	}

	if evt.RoomID == user.ManagementRoom {
		if user.HasSession() {
			_, _ = mx.sendNoticeWithMarkdown(evt.RoomID, mx.bridge.Config.Bridge.ManagementRoomText.WelcomeConnected)
		} else {
			_, _ = mx.sendNoticeWithMarkdown(evt.RoomID, mx.bridge.Config.Bridge.ManagementRoomText.WelcomeUnconnected)
		}

		additionalHelp := mx.bridge.Config.Bridge.ManagementRoomText.AdditionalHelp
		if len(additionalHelp) > 0 {
			_, _ = mx.sendNoticeWithMarkdown(evt.RoomID, additionalHelp)
		}
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
	errorMessage := fmt.Sprintf("You already have a private chat portal with me at [%[1]s](https://matrix.to/#/%[1]s)", portal.MXID)
	errorContent := format.RenderMarkdown(errorMessage, true, false)
	_, _ = intent.SendMessageEvent(roomID, event.EventMessage, errorContent)
	mx.log.Debugfln("Leaving private chat room %s as %s after accepting invite from %s as we already have chat with the user", roomID, puppet.MXID, inviter.MXID)
	_, _ = intent.LeaveRoom(roomID)
}

func (mx *MatrixHandler) createPrivatePortalFromInvite(roomID id.RoomID, inviter *User, puppet *Puppet, portal *Portal) {
	portal.MXID = roomID
	portal.Topic = PrivateChatTopic
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
	portal.log.Infofln("Created private chat portal in %s after invite from %s", roomID, inviter.MXID)
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
	portal.Update(nil)
	portal.UpdateBridgeInfo()
	_, _ = intent.SendNotice(roomID, "Private chat portal created")
}

func (mx *MatrixHandler) HandlePuppetInvite(evt *event.Event, inviter *User, puppet *Puppet) {
	intent := puppet.DefaultIntent()

	if !inviter.Whitelisted {
		puppet.log.Debugfln("Rejecting invite from %s to %s: user is not whitelisted", evt.Sender, evt.RoomID)
		_, err := intent.LeaveRoom(evt.RoomID, &mautrix.ReqLeave{
			Reason: "You're not whitelisted to use this bridge",
		})
		if err != nil {
			puppet.log.Warnfln("Failed to reject invite from %s to %s: %v", evt.Sender, evt.RoomID, err)
		}
		return
	} else if !inviter.IsLoggedIn() {
		puppet.log.Debugfln("Rejecting invite from %s to %s: user is not logged in", evt.Sender, evt.RoomID)
		_, err := intent.LeaveRoom(evt.RoomID, &mautrix.ReqLeave{
			Reason: "You're not logged into this bridge",
		})
		if err != nil {
			puppet.log.Warnfln("Failed to reject invite from %s to %s: %v", evt.Sender, evt.RoomID, err)
		}
		return
	}

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
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()

	if mx.bridge.Crypto != nil {
		mx.bridge.Crypto.HandleMemberEvent(evt)
	}

	content := evt.Content.AsMember()
	if content.Membership == event.MembershipInvite && id.UserID(evt.GetStateKey()) == mx.as.BotMXID() {
		mx.HandleBotInvite(evt)
		return
	}

	if mx.shouldIgnoreEvent(evt) {
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}
	isSelf := id.UserID(evt.GetStateKey()) == evt.Sender
	puppet := mx.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		if puppet != nil && content.Membership == event.MembershipInvite {
			mx.HandlePuppetInvite(evt, user, puppet)
		}
		return
	} else if !user.Whitelisted || !user.IsLoggedIn() {
		return
	}

	if content.Membership == event.MembershipLeave {
		if evt.Unsigned.PrevContent != nil {
			_ = evt.Unsigned.PrevContent.ParseRaw(evt.Type)
			prevContent, ok := evt.Unsigned.PrevContent.Parsed.(*event.MemberEventContent)
			if ok && prevContent.Membership != "join" {
				return
			}
		}
		if isSelf {
			portal.HandleMatrixLeave(user)
		} else if puppet != nil {
			portal.HandleMatrixKick(user, puppet)
		}
	} else if content.Membership == event.MembershipInvite && !isSelf && puppet != nil {
		portal.HandleMatrixInvite(user, puppet)
	}
}

func (mx *MatrixHandler) HandleRoomMetadata(evt *event.Event) {
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()
	if mx.shouldIgnoreEvent(evt) {
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil || !user.Whitelisted || !user.IsLoggedIn() {
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil || portal.IsPrivateChat() {
		return
	}

	portal.HandleMatrixMeta(user, evt)
}

func (mx *MatrixHandler) shouldIgnoreEvent(evt *event.Event) bool {
	if _, isPuppet := mx.bridge.ParsePuppetMXID(evt.Sender); evt.Sender == mx.bridge.Bot.UserID || isPuppet {
		return true
	}
	if val, ok := evt.Content.Raw[doublePuppetKey]; ok && val == doublePuppetValue && mx.bridge.GetPuppetByCustomMXID(evt.Sender) != nil {
		return true
	}
	user := mx.bridge.GetUserByMXID(evt.Sender)
	if !user.RelayWhitelisted {
		return true
	}
	return false
}

const sessionWaitTimeout = 5 * time.Second

func (mx *MatrixHandler) HandleEncrypted(evt *event.Event) {
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()
	if mx.shouldIgnoreEvent(evt) || mx.bridge.Crypto == nil {
		return
	}

	decrypted, err := mx.bridge.Crypto.Decrypt(evt)
	decryptionRetryCount := 0
	if errors.Is(err, NoSessionFound) {
		content := evt.Content.AsEncrypted()
		mx.log.Debugfln("Couldn't find session %s trying to decrypt %s, waiting %d seconds...", content.SessionID, evt.ID, int(sessionWaitTimeout.Seconds()))
		mx.as.SendErrorMessageSendCheckpoint(evt, appservice.StepDecrypted, err, false, decryptionRetryCount)
		decryptionRetryCount++
		if mx.bridge.Crypto.WaitForSession(evt.RoomID, content.SenderKey, content.SessionID, sessionWaitTimeout) {
			mx.log.Debugfln("Got session %s after waiting, trying to decrypt %s again", content.SessionID, evt.ID)
			decrypted, err = mx.bridge.Crypto.Decrypt(evt)
		} else {
			mx.as.SendErrorMessageSendCheckpoint(evt, appservice.StepDecrypted, fmt.Errorf("didn't receive encryption keys"), false, decryptionRetryCount)
			go mx.waitLongerForSession(evt)
			return
		}
	}
	if err != nil {
		mx.as.SendErrorMessageSendCheckpoint(evt, appservice.StepDecrypted, err, true, decryptionRetryCount)

		mx.log.Warnfln("Failed to decrypt %s: %v", evt.ID, err)
		_, _ = mx.bridge.Bot.SendNotice(evt.RoomID, fmt.Sprintf(
			"\u26a0 Your message was not bridged: %v", err))
		return
	}
	mx.as.SendMessageSendCheckpoint(decrypted, appservice.StepDecrypted, decryptionRetryCount)
	mx.bridge.EventProcessor.Dispatch(decrypted)
}

func (mx *MatrixHandler) waitLongerForSession(evt *event.Event) {
	const extendedTimeout = sessionWaitTimeout * 3

	content := evt.Content.AsEncrypted()
	mx.log.Debugfln("Couldn't find session %s trying to decrypt %s, waiting %d more seconds...",
		content.SessionID, evt.ID, int(extendedTimeout.Seconds()))

	go mx.bridge.Crypto.RequestSession(evt.RoomID, content.SenderKey, content.SessionID, evt.Sender, content.DeviceID)

	resp, err := mx.bridge.Bot.SendNotice(evt.RoomID, fmt.Sprintf(
		"\u26a0 Your message was not bridged: the bridge hasn't received the decryption keys. "+
			"The bridge will retry for %d seconds. If this error keeps happening, try restarting your client.",
		int(extendedTimeout.Seconds())))
	if err != nil {
		mx.log.Errorfln("Failed to send decryption error to %s: %v", evt.RoomID, err)
	}
	update := event.MessageEventContent{MsgType: event.MsgNotice}

	if mx.bridge.Crypto.WaitForSession(evt.RoomID, content.SenderKey, content.SessionID, extendedTimeout) {
		mx.log.Debugfln("Got session %s after waiting more, trying to decrypt %s again", content.SessionID, evt.ID)
		decrypted, err := mx.bridge.Crypto.Decrypt(evt)
		if err == nil {
			mx.as.SendMessageSendCheckpoint(decrypted, appservice.StepDecrypted, 2)
			mx.bridge.EventProcessor.Dispatch(decrypted)
			_, _ = mx.bridge.Bot.RedactEvent(evt.RoomID, resp.EventID)
			return
		}
		mx.log.Warnfln("Failed to decrypt %s: %v", evt.ID, err)
		mx.as.SendErrorMessageSendCheckpoint(evt, appservice.StepDecrypted, err, true, 2)
		update.Body = fmt.Sprintf("\u26a0 Your message was not bridged: %v", err)
	} else {
		mx.log.Debugfln("Didn't get %s, giving up on %s", content.SessionID, evt.ID)
		mx.as.SendErrorMessageSendCheckpoint(evt, appservice.StepDecrypted, fmt.Errorf("didn't receive encryption keys"), true, 2)
		update.Body = "\u26a0 Your message was not bridged: the bridge hasn't received the decryption keys. " +
			"If this error keeps happening, try restarting your client."
	}

	newContent := update
	update.NewContent = &newContent
	if resp != nil {
		update.RelatesTo = &event.RelatesTo{
			Type:    event.RelReplace,
			EventID: resp.EventID,
		}
	}
	_, err = mx.bridge.Bot.SendMessageEvent(evt.RoomID, event.EventMessage, &update)
	if err != nil {
		mx.log.Debugfln("Failed to update decryption error notice %s: %v", resp.EventID, err)
	}
}

func (mx *MatrixHandler) HandleMessage(evt *event.Event) {
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()
	if mx.shouldIgnoreEvent(evt) {
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}

	content := evt.Content.AsMessage()
	content.RemoveReplyFallback()
	if user.Whitelisted && content.MsgType == event.MsgText {
		commandPrefix := mx.bridge.Config.Bridge.CommandPrefix
		hasCommandPrefix := strings.HasPrefix(content.Body, commandPrefix)
		if hasCommandPrefix {
			content.Body = strings.TrimLeft(content.Body[len(commandPrefix):], " ")
		}
		if hasCommandPrefix || evt.RoomID == user.ManagementRoom {
			mx.cmd.Handle(evt.RoomID, evt.ID, user, content.Body, content.GetReplyTo())
			return
		}
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil && (user.Whitelisted || portal.HasRelaybot()) {
		portal.matrixMessages <- PortalMatrixMessage{user: user, evt: evt}
	}
}

func (mx *MatrixHandler) HandleReaction(evt *event.Event) {
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()
	if mx.shouldIgnoreEvent(evt) {
		return
	}

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil || !user.Whitelisted || !user.IsLoggedIn() {
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		return
	} else if portal.IsPrivateChat() && user.JID.User != portal.Key.Receiver.User {
		// One user can only react once, so we don't use the relay user for reactions
		return
	}

	content := evt.Content.AsReaction()
	if strings.Contains(content.RelatesTo.Key, "retry") || strings.HasPrefix(content.RelatesTo.Key, "\u267b") { // ♻️
		if retryRequested, _ := portal.requestMediaRetry(user, content.RelatesTo.EventID, nil); retryRequested {
			_, _ = portal.MainIntent().RedactEvent(portal.MXID, evt.ID, mautrix.ReqRedact{
				Reason: "requested media from phone",
			})
			// Errored media, don't try to send as reaction
			return
		}
	}
	portal.HandleMatrixReaction(user, evt)
}

func (mx *MatrixHandler) HandleRedaction(evt *event.Event) {
	defer mx.bridge.Metrics.TrackMatrixEvent(evt.Type)()

	user := mx.bridge.GetUserByMXID(evt.Sender)
	if user == nil {
		return
	}

	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal != nil && (user.Whitelisted || portal.HasRelaybot()) {
		portal.matrixMessages <- PortalMatrixMessage{user: user, evt: evt}
	}
}

func (mx *MatrixHandler) HandlePresence(evt *event.Event) {
	user := mx.bridge.GetUserByMXIDIfExists(evt.Sender)
	if user == nil || !user.IsLoggedIn() {
		return
	}
	customPuppet := mx.bridge.GetPuppetByCustomMXID(user.MXID)
	// TODO move this flag to the user and/or portal data
	if customPuppet != nil && !customPuppet.EnablePresence {
		return
	}

	presence := types.PresenceAvailable
	if evt.Content.AsPresence().Presence != event.PresenceOnline {
		presence = types.PresenceUnavailable
		user.log.Debugln("Marking offline")
	} else {
		user.log.Debugln("Marking online")
	}
	user.lastPresence = presence
	if user.Client.Store.PushName != "" {
		err := user.Client.SendPresence(presence)
		if err != nil {
			user.log.Warnln("Failed to set presence:", err)
		}
	}
}

func (mx *MatrixHandler) HandleReceipt(evt *event.Event) {
	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		return
	}

	for eventID, receipts := range *evt.Content.AsReceipt() {
		for userID, receipt := range receipts.Read {
			if user := mx.bridge.GetUserByMXIDIfExists(userID); user == nil {
				// Not a bridge user
			} else if customPuppet := mx.bridge.GetPuppetByCustomMXID(user.MXID); customPuppet != nil && !customPuppet.EnableReceipts {
				// TODO move this flag to the user and/or portal data
				continue
			} else if val, ok := receipt.Extra[doublePuppetKey].(string); ok && customPuppet != nil && val == doublePuppetValue {
				// Ignore double puppeted read receipts.
				user.log.Debugfln("Ignoring double puppeted read receipt %+v", evt.Content.Raw)
				// But do start disappearing messages, because the user read the chat
				portal.ScheduleDisappearing()
			} else {
				portal.HandleMatrixReadReceipt(user, eventID, time.UnixMilli(receipt.Timestamp), true)
			}
		}
	}
}

func (mx *MatrixHandler) HandleTyping(evt *event.Event) {
	portal := mx.bridge.GetPortalByMXID(evt.RoomID)
	if portal == nil {
		return
	}
	portal.HandleMatrixTyping(evt.Content.AsTyping().UserIDs)
}
