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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/tidwall/gjson"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/commands"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

type WrappedCommandEvent struct {
	*commands.Event
	Bridge *WABridge
	User   *User
	Portal *Portal
}

func (br *WABridge) RegisterCommands() {
	proc := br.CommandProcessor.(*commands.Processor)
	proc.AddHandlers(
		cmdSetRelay,
		cmdUnsetRelay,
		cmdInviteLink,
		cmdResolveLink,
		cmdJoin,
		cmdAccept,
		cmdCreate,
		cmdLogin,
		cmdLogout,
		cmdTogglePresence,
		cmdDeleteSession,
		cmdReconnect,
		cmdDisconnect,
		cmdPing,
		cmdDeletePortal,
		cmdDeleteAllPortals,
		cmdBackfill,
		cmdList,
		cmdSearch,
		cmdOpen,
		cmdPM,
		cmdSync,
		cmdDisappearingTimer,
	)
}

func wrapCommand(handler func(*WrappedCommandEvent)) func(*commands.Event) {
	return func(ce *commands.Event) {
		user := ce.User.(*User)
		var portal *Portal
		if ce.Portal != nil {
			portal = ce.Portal.(*Portal)
		}
		br := ce.Bridge.Child.(*WABridge)
		handler(&WrappedCommandEvent{ce, br, user, portal})
	}
}

var (
	HelpSectionConnectionManagement = commands.HelpSection{Name: "Connection management", Order: 11}
	HelpSectionCreatingPortals      = commands.HelpSection{Name: "Creating portals", Order: 15}
	HelpSectionPortalManagement     = commands.HelpSection{Name: "Portal management", Order: 20}
	HelpSectionInvites              = commands.HelpSection{Name: "Group invites", Order: 25}
	HelpSectionMiscellaneous        = commands.HelpSection{Name: "Miscellaneous", Order: 30}
)

var cmdSetRelay = &commands.FullHandler{
	Func: wrapCommand(fnSetRelay),
	Name: "set-relay",
	Help: commands.HelpMeta{
		Section:     HelpSectionPortalManagement,
		Description: "Relay messages in this room through your WhatsApp account.",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

func fnSetRelay(ce *WrappedCommandEvent) {
	if !ce.Bridge.Config.Bridge.Relay.Enabled {
		ce.Reply("Relay mode is not enabled on this instance of the bridge")
	} else if ce.Bridge.Config.Bridge.Relay.AdminOnly && !ce.User.Admin {
		ce.Reply("Only admins are allowed to enable relay mode on this instance of the bridge")
	} else {
		ce.Portal.RelayUserID = ce.User.MXID
		ce.Portal.Update(nil)
		ce.Reply("Messages from non-logged-in users in this room will now be bridged through your WhatsApp account")
	}
}

var cmdUnsetRelay = &commands.FullHandler{
	Func: wrapCommand(fnUnsetRelay),
	Name: "unset-relay",
	Help: commands.HelpMeta{
		Section:     HelpSectionPortalManagement,
		Description: "Stop relaying messages in this room.",
	},
	RequiresPortal: true,
}

func fnUnsetRelay(ce *WrappedCommandEvent) {
	if !ce.Bridge.Config.Bridge.Relay.Enabled {
		ce.Reply("Relay mode is not enabled on this instance of the bridge")
	} else if ce.Bridge.Config.Bridge.Relay.AdminOnly && !ce.User.Admin {
		ce.Reply("Only admins are allowed to enable relay mode on this instance of the bridge")
	} else {
		ce.Portal.RelayUserID = ""
		ce.Portal.Update(nil)
		ce.Reply("Messages from non-logged-in users will no longer be bridged in this room")
	}
}

var cmdInviteLink = &commands.FullHandler{
	Func: wrapCommand(fnInviteLink),
	Name: "invite-link",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Get an invite link to the current group chat, optionally regenerating the link and revoking the old link.",
		Args:        "[--reset]",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

func fnInviteLink(ce *WrappedCommandEvent) {
	reset := len(ce.Args) > 0 && strings.ToLower(ce.Args[0]) == "--reset"
	if ce.Portal.IsPrivateChat() {
		ce.Reply("Can't get invite link to private chat")
	} else if ce.Portal.IsBroadcastList() {
		ce.Reply("Can't get invite link to broadcast list")
	} else if link, err := ce.User.Client.GetGroupInviteLink(ce.Portal.Key.JID, reset); err != nil {
		ce.Reply("Failed to get invite link: %v", err)
	} else {
		ce.Reply(link)
	}
}

var cmdResolveLink = &commands.FullHandler{
	Func: wrapCommand(fnResolveLink),
	Name: "resolve-link",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Resolve a WhatsApp group invite or business message link.",
		Args:        "<_group or message link_>",
	},
	RequiresLogin: true,
}

func fnResolveLink(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `resolve-link <group or message link>`")
		return
	}
	if strings.HasPrefix(ce.Args[0], whatsmeow.InviteLinkPrefix) {
		group, err := ce.User.Client.GetGroupInfoFromLink(ce.Args[0])
		if err != nil {
			ce.Reply("Failed to get group info: %v", err)
			return
		}
		ce.Reply("That invite link points at %s (`%s`)", group.Name, group.JID)
	} else if strings.HasPrefix(ce.Args[0], whatsmeow.BusinessMessageLinkPrefix) || strings.HasPrefix(ce.Args[0], whatsmeow.BusinessMessageLinkDirectPrefix) {
		target, err := ce.User.Client.ResolveBusinessMessageLink(ce.Args[0])
		if err != nil {
			ce.Reply("Failed to get business info: %v", err)
			return
		}
		message := ""
		if len(target.Message) > 0 {
			parts := strings.Split(target.Message, "\n")
			for i, part := range parts {
				parts[i] = "> " + html.EscapeString(part)
			}
			message = fmt.Sprintf(" The following prefilled message is attached:\n\n%s", strings.Join(parts, "\n"))
		}
		ce.Reply("That link points at %s (+%s).%s", target.PushName, target.JID.User, message)
	} else {
		ce.Reply("That doesn't look like a group invite link nor a business message link.")
	}
}

var cmdJoin = &commands.FullHandler{
	Func: wrapCommand(fnJoin),
	Name: "join",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Join a group chat with an invite link.",
		Args:        "<_invite link_>",
	},
	RequiresLogin: true,
}

func fnJoin(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `join <invite link>`")
		return
	} else if !strings.HasPrefix(ce.Args[0], whatsmeow.InviteLinkPrefix) {
		ce.Reply("That doesn't look like a WhatsApp invite link")
		return
	}

	jid, err := ce.User.Client.JoinGroupWithLink(ce.Args[0])
	if err != nil {
		ce.Reply("Failed to join group: %v", err)
		return
	}
	ce.Log.Debugln("%s successfully joined group %s", ce.User.MXID, jid)
	ce.Reply("Successfully joined group `%s`, the portal should be created momentarily", jid)
}

func tryDecryptEvent(crypto bridge.Crypto, evt *event.Event) (json.RawMessage, error) {
	var data json.RawMessage
	if evt.Type != event.EventEncrypted {
		data = evt.Content.VeryRaw
	} else {
		err := evt.Content.ParseRaw(evt.Type)
		if err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
			return nil, err
		}
		decrypted, err := crypto.Decrypt(evt)
		if err != nil {
			return nil, err
		}
		data = decrypted.Content.VeryRaw
	}
	return data, nil
}

func parseInviteMeta(data json.RawMessage) (*InviteMeta, error) {
	result := gjson.GetBytes(data, escapedInviteMetaField)
	if !result.Exists() || !result.IsObject() {
		return nil, nil
	}
	var meta InviteMeta
	err := json.Unmarshal([]byte(result.Raw), &meta)
	if err != nil {
		return nil, nil
	}
	return &meta, nil
}

var cmdAccept = &commands.FullHandler{
	Func: wrapCommand(fnAccept),
	Name: "accept",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Accept a group invite. This can only be used in reply to a group invite message.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

func fnAccept(ce *WrappedCommandEvent) {
	if len(ce.ReplyTo) == 0 {
		ce.Reply("You must reply to a group invite message when using this command.")
	} else if evt, err := ce.Portal.MainIntent().GetEvent(ce.RoomID, ce.ReplyTo); err != nil {
		ce.Log.Errorln("Failed to get event %s to handle !wa accept command: %v", ce.ReplyTo, err)
		ce.Reply("Failed to get reply event")
	} else if rawContent, err := tryDecryptEvent(ce.Bridge.Crypto, evt); err != nil {
		ce.Log.Errorln("Failed to decrypt event %s to handle !wa accept command: %v", ce.ReplyTo, err)
		ce.Reply("Failed to decrypt reply event")
	} else if meta, err := parseInviteMeta(rawContent); err != nil || meta == nil {
		ce.Reply("That doesn't look like a group invite message.")
	} else if meta.Inviter.User == ce.User.JID.User {
		ce.Reply("You can't accept your own invites")
	} else if err = ce.User.Client.JoinGroupWithInvite(meta.JID, meta.Inviter, meta.Code, meta.Expiration); err != nil {
		ce.Reply("Failed to accept group invite: %v", err)
	} else {
		ce.Reply("Successfully accepted the invite, the portal should be created momentarily")
	}
}

var cmdCreate = &commands.FullHandler{
	Func: wrapCommand(fnCreate),
	Name: "create",
	Help: commands.HelpMeta{
		Section:     HelpSectionCreatingPortals,
		Description: "Create a WhatsApp group chat for the current Matrix room.",
	},
	RequiresLogin: true,
}

func fnCreate(ce *WrappedCommandEvent) {
	if ce.Portal != nil {
		ce.Reply("This is already a portal room")
		return
	}

	members, err := ce.Bot.JoinedMembers(ce.RoomID)
	if err != nil {
		ce.Reply("Failed to get room members: %v", err)
		return
	}

	var roomNameEvent event.RoomNameEventContent
	err = ce.Bot.StateEvent(ce.RoomID, event.StateRoomName, "", &roomNameEvent)
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		ce.Log.Errorln("Failed to get room name to create group:", err)
		ce.Reply("Failed to get room name")
		return
	} else if len(roomNameEvent.Name) == 0 {
		ce.Reply("Please set a name for the room first")
		return
	}

	var encryptionEvent event.EncryptionEventContent
	err = ce.Bot.StateEvent(ce.RoomID, event.StateEncryption, "", &encryptionEvent)
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		ce.Reply("Failed to get room encryption status")
		return
	}

	var participants []types.JID
	participantDedup := make(map[types.JID]bool)
	participantDedup[ce.User.JID.ToNonAD()] = true
	participantDedup[types.EmptyJID] = true
	for userID := range members.Joined {
		jid, ok := ce.Bridge.ParsePuppetMXID(userID)
		if !ok {
			user := ce.Bridge.GetUserByMXID(userID)
			if user != nil && !user.JID.IsEmpty() {
				jid = user.JID.ToNonAD()
			}
		}
		if !participantDedup[jid] {
			participantDedup[jid] = true
			participants = append(participants, jid)
		}
	}

	ce.Log.Infofln("Creating group for %s with name %s and participants %+v", ce.RoomID, roomNameEvent.Name, participants)
	resp, err := ce.User.Client.CreateGroup(roomNameEvent.Name, participants, "")
	if err != nil {
		ce.Reply("Failed to create group: %v", err)
		return
	}
	portal := ce.User.GetPortalByJID(resp.JID)
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if len(portal.MXID) != 0 {
		portal.log.Warnln("Detected race condition in room creation")
		// TODO race condition, clean up the old room
	}
	portal.MXID = ce.RoomID
	portal.Name = roomNameEvent.Name
	portal.Encrypted = encryptionEvent.Algorithm == id.AlgorithmMegolmV1
	if !portal.Encrypted && ce.Bridge.Config.Bridge.Encryption.Default {
		_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateEncryption, "", portal.GetEncryptionEventContent())
		if err != nil {
			portal.log.Warnln("Failed to enable encryption in room:", err)
			if errors.Is(err, mautrix.MForbidden) {
				ce.Reply("I don't seem to have permission to enable encryption in this room.")
			} else {
				ce.Reply("Failed to enable encryption in room: %v", err)
			}
		}
		portal.Encrypted = true
	}

	portal.Update(nil)
	portal.UpdateBridgeInfo()

	ce.Reply("Successfully created WhatsApp group %s", portal.Key.JID)
}

var cmdLogin = &commands.FullHandler{
	Func: wrapCommand(fnLogin),
	Name: "login",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Link the bridge to your WhatsApp account as a web client.",
	},
}

func fnLogin(ce *WrappedCommandEvent) {
	if ce.User.Session != nil {
		if ce.User.IsConnected() {
			ce.Reply("You're already logged in")
		} else {
			ce.Reply("You're already logged in. Perhaps you wanted to `reconnect`?")
		}
		return
	}

	qrChan, err := ce.User.Login(context.Background())
	if err != nil {
		ce.User.log.Errorf("Failed to log in:", err)
		ce.Reply("Failed to log in: %v", err)
		return
	}

	var qrEventID id.EventID
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelSuccess.Event:
			jid := ce.User.Client.Store.ID
			ce.Reply("Successfully logged in as +%s (device #%d)", jid.User, jid.Device)
		case whatsmeow.QRChannelTimeout.Event:
			ce.Reply("QR code timed out. Please restart the login.")
		case whatsmeow.QRChannelErrUnexpectedEvent.Event:
			ce.Reply("Failed to log in: unexpected connection event from server")
		case whatsmeow.QRChannelClientOutdated.Event:
			ce.Reply("Failed to log in: outdated client. The bridge must be updated to continue.")
		case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
			ce.Reply("Please enable the WhatsApp multidevice beta and scan the QR code again.")
		case "error":
			ce.Reply("Failed to log in: %v", item.Error)
		case "code":
			qrEventID = ce.User.sendQR(ce, item.Code, qrEventID)
		}
	}
	_, _ = ce.Bot.RedactEvent(ce.RoomID, qrEventID)
}

func (user *User) sendQR(ce *WrappedCommandEvent, code string, prevEvent id.EventID) id.EventID {
	url, ok := user.uploadQR(ce, code)
	if !ok {
		return prevEvent
	}
	content := event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    code,
		URL:     url.CUString(),
	}
	if len(prevEvent) != 0 {
		content.SetEdit(prevEvent)
	}
	resp, err := ce.Bot.SendMessageEvent(ce.RoomID, event.EventMessage, &content)
	if err != nil {
		user.log.Errorln("Failed to send edited QR code to user:", err)
	} else if len(prevEvent) == 0 {
		prevEvent = resp.EventID
	}
	return prevEvent
}

func (user *User) uploadQR(ce *WrappedCommandEvent, code string) (id.ContentURI, bool) {
	qrCode, err := qrcode.Encode(code, qrcode.Low, 256)
	if err != nil {
		user.log.Errorln("Failed to encode QR code:", err)
		ce.Reply("Failed to encode QR code: %v", err)
		return id.ContentURI{}, false
	}

	bot := user.bridge.AS.BotClient()

	resp, err := bot.UploadBytes(qrCode, "image/png")
	if err != nil {
		user.log.Errorln("Failed to upload QR code:", err)
		ce.Reply("Failed to upload QR code: %v", err)
		return id.ContentURI{}, false
	}
	return resp.ContentURI, true
}

var cmdLogout = &commands.FullHandler{
	Func: wrapCommand(fnLogout),
	Name: "logout",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Unlink the bridge from your WhatsApp account.",
	},
}

func fnLogout(ce *WrappedCommandEvent) {
	if ce.User.Session == nil {
		ce.Reply("You're not logged in.")
		return
	} else if !ce.User.IsLoggedIn() {
		ce.Reply("You are not connected to WhatsApp. Use the `reconnect` command to reconnect, or `delete-session` to forget all login information.")
		return
	}
	puppet := ce.Bridge.GetPuppetByJID(ce.User.JID)
	if puppet.CustomMXID != "" {
		err := puppet.SwitchCustomMXID("", "")
		if err != nil {
			ce.User.log.Warnln("Failed to logout-matrix while logging out of WhatsApp:", err)
		}
	}
	err := ce.User.Client.Logout()
	if err != nil {
		ce.User.log.Warnln("Error while logging out:", err)
		ce.Reply("Unknown error while logging out: %v", err)
		return
	}
	ce.User.Session = nil
	ce.User.removeFromJIDMap(bridge.State{StateEvent: bridge.StateLoggedOut})
	ce.User.DeleteConnection()
	ce.User.DeleteSession()
	ce.Reply("Logged out successfully.")
}

var cmdTogglePresence = &commands.FullHandler{
	Func: wrapCommand(fnTogglePresence),
	Name: "toggle-presence",
	Help: commands.HelpMeta{
		Section:     HelpSectionConnectionManagement,
		Description: "Toggle bridging of presence or read receipts.",
	},
}

func fnTogglePresence(ce *WrappedCommandEvent) {
	if ce.User.Session == nil {
		ce.Reply("You're not logged in.")
		return
	}
	customPuppet := ce.Bridge.GetPuppetByCustomMXID(ce.User.MXID)
	if customPuppet == nil {
		ce.Reply("You're not logged in with your Matrix account.")
		return
	}
	customPuppet.EnablePresence = !customPuppet.EnablePresence
	var newPresence types.Presence
	if customPuppet.EnablePresence {
		newPresence = types.PresenceAvailable
		ce.Reply("Enabled presence bridging")
	} else {
		newPresence = types.PresenceUnavailable
		ce.Reply("Disabled presence bridging")
	}
	if ce.User.IsLoggedIn() {
		err := ce.User.Client.SendPresence(newPresence)
		if err != nil {
			ce.User.log.Warnln("Failed to set presence:", err)
		}
	}
	customPuppet.Update()
}

var cmdDeleteSession = &commands.FullHandler{
	Func: wrapCommand(fnDeleteSession),
	Name: "delete-session",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAuth,
		Description: "Delete session information and disconnect from WhatsApp without sending a logout request.",
	},
}

func fnDeleteSession(ce *WrappedCommandEvent) {
	if ce.User.Session == nil && ce.User.Client == nil {
		ce.Reply("Nothing to purge: no session information stored and no active connection.")
		return
	}
	ce.User.removeFromJIDMap(bridge.State{StateEvent: bridge.StateLoggedOut})
	ce.User.DeleteConnection()
	ce.User.DeleteSession()
	ce.Reply("Session information purged")
}

var cmdReconnect = &commands.FullHandler{
	Func: wrapCommand(fnReconnect),
	Name: "reconnect",
	Help: commands.HelpMeta{
		Section:     HelpSectionConnectionManagement,
		Description: "Reconnect to WhatsApp.",
	},
}

func fnReconnect(ce *WrappedCommandEvent) {
	if ce.User.Client == nil {
		if ce.User.Session == nil {
			ce.Reply("You're not logged into WhatsApp. Please log in first.")
		} else {
			ce.User.Connect()
			ce.Reply("Started connecting to WhatsApp")
		}
	} else {
		ce.User.DeleteConnection()
		ce.User.BridgeState.Send(bridge.State{StateEvent: bridge.StateTransientDisconnect, Error: WANotConnected})
		ce.User.Connect()
		ce.Reply("Restarted connection to WhatsApp")
	}
}

var cmdDisconnect = &commands.FullHandler{
	Func: wrapCommand(fnDisconnect),
	Name: "disconnect",
	Help: commands.HelpMeta{
		Section:     HelpSectionConnectionManagement,
		Description: "Disconnect from WhatsApp (without logging out).",
	},
}

func fnDisconnect(ce *WrappedCommandEvent) {
	if ce.User.Client == nil {
		ce.Reply("You don't have a WhatsApp connection.")
		return
	}
	ce.User.DeleteConnection()
	ce.Reply("Successfully disconnected. Use the `reconnect` command to reconnect.")
	ce.User.BridgeState.Send(bridge.State{StateEvent: bridge.StateBadCredentials, Error: WANotConnected})
}

var cmdPing = &commands.FullHandler{
	Func: wrapCommand(fnPing),
	Name: "ping",
	Help: commands.HelpMeta{
		Section:     HelpSectionConnectionManagement,
		Description: "Check your connection to WhatsApp.",
	},
}

func fnPing(ce *WrappedCommandEvent) {
	if ce.User.Session == nil {
		if ce.User.Client != nil {
			ce.Reply("Connected to WhatsApp, but not logged in.")
		} else {
			ce.Reply("You're not logged into WhatsApp.")
		}
	} else if ce.User.Client == nil || !ce.User.Client.IsConnected() {
		ce.Reply("You're logged in as +%s (device #%d), but you don't have a WhatsApp connection.", ce.User.JID.User, ce.User.JID.Device)
	} else {
		ce.Reply("Logged in as +%s (device #%d), connection to WhatsApp OK (probably)", ce.User.JID.User, ce.User.JID.Device)
		if !ce.User.PhoneRecentlySeen(false) {
			ce.Reply("Phone hasn't been seen in %s", formatDisconnectTime(time.Now().Sub(ce.User.PhoneLastSeen)))
		}
	}
}

func canDeletePortal(portal *Portal, userID id.UserID) bool {
	if len(portal.MXID) == 0 {
		return false
	}

	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Errorfln("Failed to get joined members to check if portal can be deleted by %s: %v", userID, err)
		return false
	}
	for otherUser := range members.Joined {
		_, isPuppet := portal.bridge.ParsePuppetMXID(otherUser)
		if isPuppet || otherUser == portal.bridge.Bot.UserID || otherUser == userID {
			continue
		}
		user := portal.bridge.GetUserByMXID(otherUser)
		if user != nil && user.Session != nil {
			return false
		}
	}
	return true
}

var cmdDeletePortal = &commands.FullHandler{
	Func: wrapCommand(fnDeletePortal),
	Name: "delete-portal",
	Help: commands.HelpMeta{
		Section:     HelpSectionPortalManagement,
		Description: "Delete the current portal. If the portal is used by other people, this is limited to bridge admins.",
	},
	RequiresPortal: true,
}

func fnDeletePortal(ce *WrappedCommandEvent) {
	if !ce.User.Admin && !canDeletePortal(ce.Portal, ce.User.MXID) {
		ce.Reply("Only bridge admins can delete portals with other Matrix users")
		return
	}

	ce.Portal.log.Infoln(ce.User.MXID, "requested deletion of portal.")
	ce.Portal.Delete()
	ce.Portal.Cleanup(false)
}

var cmdDeleteAllPortals = &commands.FullHandler{
	Func: wrapCommand(fnDeleteAllPortals),
	Name: "delete-all-portals",
	Help: commands.HelpMeta{
		Section:     HelpSectionPortalManagement,
		Description: "Delete all portals.",
	},
}

func fnDeleteAllPortals(ce *WrappedCommandEvent) {
	portals := ce.Bridge.GetAllPortals()
	var portalsToDelete []*Portal

	if ce.User.Admin {
		portalsToDelete = portals
	} else {
		portalsToDelete = portals[:0]
		for _, portal := range portals {
			if canDeletePortal(portal, ce.User.MXID) {
				portalsToDelete = append(portalsToDelete, portal)
			}
		}
	}
	if len(portalsToDelete) == 0 {
		ce.Reply("Didn't find any portals to delete")
		return
	}

	leave := func(portal *Portal) {
		if len(portal.MXID) > 0 {
			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{
				Reason: "Deleting portal",
				UserID: ce.User.MXID,
			})
		}
	}
	customPuppet := ce.Bridge.GetPuppetByCustomMXID(ce.User.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		intent := customPuppet.CustomIntent()
		leave = func(portal *Portal) {
			if len(portal.MXID) > 0 {
				_, _ = intent.LeaveRoom(portal.MXID)
				_, _ = intent.ForgetRoom(portal.MXID)
			}
		}
	}
	ce.Reply("Found %d portals, deleting...", len(portalsToDelete))
	for _, portal := range portalsToDelete {
		portal.Delete()
		leave(portal)
	}
	ce.Reply("Finished deleting portal info. Now cleaning up rooms in background.")

	go func() {
		for _, portal := range portalsToDelete {
			portal.Cleanup(false)
		}
		ce.Reply("Finished background cleanup of deleted portal rooms.")
	}()
}

var cmdBackfill = &commands.FullHandler{
	Func: wrapCommand(fnBackfill),
	Name: "backfill",
	Help: commands.HelpMeta{
		Section:     HelpSectionPortalManagement,
		Description: "Backfill all messages the portal.",
		Args:        "[_batch size_] [_batch delay_]",
	},
	RequiresPortal: true,
}

func fnBackfill(ce *WrappedCommandEvent) {
	if !ce.Bridge.Config.Bridge.HistorySync.Backfill {
		ce.Reply("Backfill is not enabled for this bridge.")
		return
	}
	batchSize := 100
	batchDelay := 5
	if len(ce.Args) >= 1 {
		var err error
		batchSize, err = strconv.Atoi(ce.Args[0])
		if err != nil || batchSize < 1 {
			ce.Reply("\"%s\" isn't a valid batch size", ce.Args[0])
			return
		}
	}
	if len(ce.Args) >= 2 {
		var err error
		batchDelay, err = strconv.Atoi(ce.Args[0])
		if err != nil || batchSize < 0 {
			ce.Reply("\"%s\" isn't a valid batch delay", ce.Args[1])
			return
		}
	}
	backfillMessages := ce.Portal.bridge.DB.Backfill.NewWithValues(ce.User.MXID, database.BackfillImmediate, 0, &ce.Portal.Key, nil, batchSize, -1, batchDelay)
	backfillMessages.Insert()

	ce.User.BackfillQueue.ReCheck()
}

func matchesQuery(str string, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(str), query)
}

func formatContacts(bridge *WABridge, input map[types.JID]types.ContactInfo, query string) (result []string) {
	hasQuery := len(query) > 0
	for jid, contact := range input {
		if len(contact.FullName) == 0 {
			continue
		}
		puppet := bridge.GetPuppetByJID(jid)
		pushName := contact.PushName
		if len(pushName) == 0 {
			pushName = contact.FullName
		}

		if !hasQuery || matchesQuery(pushName, query) || matchesQuery(contact.FullName, query) || matchesQuery(jid.User, query) {
			result = append(result, fmt.Sprintf("* %s / [%s](https://matrix.to/#/%s) - `+%s`", contact.FullName, pushName, puppet.MXID, jid.User))
		}
	}
	sort.Sort(sort.StringSlice(result))
	return
}

func formatGroups(input []*types.GroupInfo, query string) (result []string) {
	hasQuery := len(query) > 0
	for _, group := range input {
		if !hasQuery || matchesQuery(group.GroupName.Name, query) || matchesQuery(group.JID.User, query) {
			result = append(result, fmt.Sprintf("* %s - `%s`", group.GroupName.Name, group.JID.User))
		}
	}
	sort.Sort(sort.StringSlice(result))
	return
}

var cmdList = &commands.FullHandler{
	Func: wrapCommand(fnList),
	Name: "list",
	Help: commands.HelpMeta{
		Section:     HelpSectionMiscellaneous,
		Description: "Get a list of all contacts and groups.",
		Args:        "<`contacts`|`groups`> [_page_] [_items per page_]",
	},
	RequiresLogin: true,
}

func fnList(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `list <contacts|groups> [page] [items per page]`")
		return
	}
	mode := strings.ToLower(ce.Args[0])
	if mode[0] != 'g' && mode[0] != 'c' {
		ce.Reply("**Usage:** `list <contacts|groups> [page] [items per page]`")
		return
	}
	var err error
	page := 1
	max := 100
	if len(ce.Args) > 1 {
		page, err = strconv.Atoi(ce.Args[1])
		if err != nil || page <= 0 {
			ce.Reply("\"%s\" isn't a valid page number", ce.Args[1])
			return
		}
	}
	if len(ce.Args) > 2 {
		max, err = strconv.Atoi(ce.Args[2])
		if err != nil || max <= 0 {
			ce.Reply("\"%s\" isn't a valid number of items per page", ce.Args[2])
			return
		} else if max > 400 {
			ce.Reply("Warning: a high number of items per page may fail to send a reply")
		}
	}

	contacts := mode[0] == 'c'
	typeName := "Groups"
	var result []string
	if contacts {
		typeName = "Contacts"
		contactList, err := ce.User.Client.Store.Contacts.GetAllContacts()
		if err != nil {
			ce.Reply("Failed to get contacts: %s", err)
			return
		}
		result = formatContacts(ce.User.bridge, contactList, "")
	} else {
		groupList, err := ce.User.Client.GetJoinedGroups()
		if err != nil {
			ce.Reply("Failed to get groups: %s", err)
			return
		}
		result = formatGroups(groupList, "")
	}

	if len(result) == 0 {
		ce.Reply("No %s found", strings.ToLower(typeName))
		return
	}
	pages := int(math.Ceil(float64(len(result)) / float64(max)))
	if (page-1)*max >= len(result) {
		if pages == 1 {
			ce.Reply("There is only 1 page of %s", strings.ToLower(typeName))
		} else {
			ce.Reply("There are %d pages of %s", pages, strings.ToLower(typeName))
		}
		return
	}
	lastIndex := page * max
	if lastIndex > len(result) {
		lastIndex = len(result)
	}
	result = result[(page-1)*max : lastIndex]
	ce.Reply("### %s (page %d of %d)\n\n%s", typeName, page, pages, strings.Join(result, "\n"))
}

var cmdSearch = &commands.FullHandler{
	Func: wrapCommand(fnSearch),
	Name: "search",
	Help: commands.HelpMeta{
		Section:     HelpSectionMiscellaneous,
		Description: "Search for contacts or groups.",
		Args:        "<_query_>",
	},
	RequiresLogin: true,
}

func fnSearch(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `search <query>`")
		return
	}

	contactList, err := ce.User.Client.Store.Contacts.GetAllContacts()
	if err != nil {
		ce.Reply("Failed to get contacts: %s", err)
		return
	}
	groupList, err := ce.User.Client.GetJoinedGroups()
	if err != nil {
		ce.Reply("Failed to get groups: %s", err)
		return
	}

	query := strings.ToLower(strings.TrimSpace(strings.Join(ce.Args, " ")))
	formattedContacts := strings.Join(formatContacts(ce.User.bridge, contactList, query), "\n")
	formattedGroups := strings.Join(formatGroups(groupList, query), "\n")

	result := make([]string, 0, 2)
	if len(formattedContacts) > 0 {
		result = append(result, "### Contacts\n\n"+formattedContacts)
	}
	if len(formattedGroups) > 0 {
		result = append(result, "### Groups\n\n"+formattedGroups)
	}

	if len(result) == 0 {
		ce.Reply("No contacts or groups found")
		return
	}

	ce.Reply(strings.Join(result, "\n\n"))
}

var cmdOpen = &commands.FullHandler{
	Func: wrapCommand(fnOpen),
	Name: "open",
	Help: commands.HelpMeta{
		Section:     HelpSectionCreatingPortals,
		Description: "Open a group chat portal.",
		Args:        "<_group JID_>",
	},
	RequiresLogin: true,
}

func fnOpen(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `open <group JID>`")
		return
	}

	var jid types.JID
	if strings.ContainsRune(ce.Args[0], '@') {
		jid, _ = types.ParseJID(ce.Args[0])
	} else {
		jid = types.NewJID(ce.Args[0], types.GroupServer)
	}
	if jid.Server != types.GroupServer || (!strings.ContainsRune(jid.User, '-') && len(jid.User) < 15) {
		ce.Reply("That does not look like a group JID")
		return
	}

	info, err := ce.User.Client.GetGroupInfo(jid)
	if err != nil {
		ce.Reply("Failed to get group info: %v", err)
		return
	}
	ce.Log.Debugln("Importing", jid, "for", ce.User.MXID)
	portal := ce.User.GetPortalByJID(info.JID)
	if len(portal.MXID) > 0 {
		portal.UpdateMatrixRoom(ce.User, info)
		ce.Reply("Portal room synced.")
	} else {
		err = portal.CreateMatrixRoom(ce.User, info, true, true)
		if err != nil {
			ce.Reply("Failed to create room: %v", err)
		} else {
			ce.Reply("Portal room created.")
		}
	}
}

var cmdPM = &commands.FullHandler{
	Func: wrapCommand(fnPM),
	Name: "pm",
	Help: commands.HelpMeta{
		Section:     HelpSectionCreatingPortals,
		Description: "Open a private chat with the given phone number.",
		Args:        "<_international phone number_>",
	},
	RequiresLogin: true,
}

func fnPM(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `pm <international phone number>`")
		return
	}

	user := ce.User

	number := strings.Join(ce.Args, "")
	resp, err := ce.User.Client.IsOnWhatsApp([]string{number})
	if err != nil {
		ce.Reply("Failed to check if user is on WhatsApp: %v", err)
		return
	} else if len(resp) == 0 {
		ce.Reply("Didn't get a response to checking if the user is on WhatsApp")
		return
	}
	targetUser := resp[0]
	if !targetUser.IsIn {
		ce.Reply("The server said +%s is not on WhatsApp", targetUser.JID.User)
		return
	}

	portal, puppet, justCreated, err := user.StartPM(targetUser.JID, "manual PM command")
	if err != nil {
		ce.Reply("Failed to create portal room: %v", err)
	} else if !justCreated {
		ce.Reply("You already have a private chat portal with +%s at [%s](https://matrix.to/#/%s)", puppet.JID.User, puppet.Displayname, portal.MXID)
	} else {
		ce.Reply("Created portal room with +%s and invited you to it.", puppet.JID.User)
	}
}

var cmdSync = &commands.FullHandler{
	Func: wrapCommand(fnSync),
	Name: "sync",
	Help: commands.HelpMeta{
		Section:     HelpSectionMiscellaneous,
		Description: "Synchronize data from WhatsApp.",
		Args:        "<appstate/contacts/groups/space> [--create-portals]",
	},
	RequiresLogin: true,
}

func fnSync(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `sync <appstate/contacts/avatars/groups/space> [--contact-avatars] [--create-portals]`")
		return
	}
	args := strings.ToLower(strings.Join(ce.Args, " "))
	contacts := strings.Contains(args, "contacts")
	appState := strings.Contains(args, "appstate")
	space := strings.Contains(args, "space")
	groups := strings.Contains(args, "groups") || space
	createPortals := strings.Contains(args, "--create-portals")
	contactAvatars := strings.Contains(args, "--contact-avatars")
	if contactAvatars && (!contacts || appState) {
		ce.Reply("`--contact-avatars` can only be used with `sync contacts`")
		return
	}

	if appState {
		for _, name := range appstate.AllPatchNames {
			err := ce.User.Client.FetchAppState(name, true, false)
			if errors.Is(err, appstate.ErrKeyNotFound) {
				ce.Reply("Key not found error syncing app state %s: %v\n\nKey requests are sent automatically, and the sync should happen in the background after your phone responds.", name, err)
				return
			} else if err != nil {
				ce.Reply("Error syncing app state %s: %v", name, err)
			} else if name == appstate.WAPatchCriticalUnblockLow {
				ce.Reply("Synced app state %s, contact sync running in background", name)
			} else {
				ce.Reply("Synced app state %s", name)
			}
		}
	} else if contacts {
		err := ce.User.ResyncContacts(contactAvatars)
		if err != nil {
			ce.Reply("Error resyncing contacts: %v", err)
		} else {
			ce.Reply("Resynced contacts")
		}
	}
	if space {
		if !ce.Bridge.Config.Bridge.PersonalFilteringSpaces {
			ce.Reply("Personal filtering spaces are not enabled on this instance of the bridge")
			return
		}
		keys := ce.Bridge.DB.Portal.FindPrivateChatsNotInSpace(ce.User.JID)
		count := 0
		for _, key := range keys {
			portal := ce.Bridge.GetPortalByJID(key)
			portal.addToSpace(ce.User)
			count++
		}
		plural := "s"
		if count == 1 {
			plural = ""
		}
		ce.Reply("Added %d DM room%s to space", count, plural)
	}
	if groups {
		err := ce.User.ResyncGroups(createPortals)
		if err != nil {
			ce.Reply("Error resyncing groups: %v", err)
		} else {
			ce.Reply("Resynced groups")
		}
	}
}

var cmdDisappearingTimer = &commands.FullHandler{
	Func:    wrapCommand(fnDisappearingTimer),
	Name:    "disappearing-timer",
	Aliases: []string{"disappear-timer"},
	Help: commands.HelpMeta{
		Section:     HelpSectionPortalManagement,
		Description: "Set future messages in the room to disappear after the given time.",
		Args:        "<off/1d/7d/90d>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

func fnDisappearingTimer(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `disappearing-timer <off/1d/7d/90d>`")
		return
	}
	duration, ok := whatsmeow.ParseDisappearingTimerString(ce.Args[0])
	if !ok {
		ce.Reply("Invalid timer '%s'", ce.Args[0])
		return
	}
	prevExpirationTime := ce.Portal.ExpirationTime
	ce.Portal.ExpirationTime = uint32(duration.Seconds())
	err := ce.User.Client.SetDisappearingTimer(ce.Portal.Key.JID, duration)
	if err != nil {
		ce.Reply("Failed to set disappearing timer: %v", err)
		ce.Portal.ExpirationTime = prevExpirationTime
		return
	}
	ce.Portal.Update(nil)
	if !ce.Portal.IsPrivateChat() && !ce.Bridge.Config.Bridge.DisappearingMessagesInGroups {
		ce.Reply("Disappearing timer changed successfully, but this bridge is not configured to disappear messages in group chats.")
	} else {
		ce.React("✅")
	}
}
