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
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/skip2/go-qrcode"

	"maunium.net/go/maulogger/v2"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

type CommandHandler struct {
	bridge *Bridge
	log    maulogger.Logger
}

// NewCommandHandler creates a CommandHandler
func NewCommandHandler(bridge *Bridge) *CommandHandler {
	return &CommandHandler{
		bridge: bridge,
		log:    bridge.Log.Sub("Command handler"),
	}
}

// CommandEvent stores all data which might be used to handle commands
type CommandEvent struct {
	Bot     *appservice.IntentAPI
	Bridge  *Bridge
	Portal  *Portal
	Handler *CommandHandler
	RoomID  id.RoomID
	User    *User
	Command string
	Args    []string
	ReplyTo id.EventID
}

// Reply sends a reply to command as notice
func (ce *CommandEvent) Reply(msg string, args ...interface{}) {
	content := format.RenderMarkdown(fmt.Sprintf(msg, args...), true, false)
	content.MsgType = event.MsgNotice
	intent := ce.Bot
	if ce.Portal != nil && ce.Portal.IsPrivateChat() {
		intent = ce.Portal.MainIntent()
	}
	_, err := intent.SendMessageEvent(ce.RoomID, event.EventMessage, content)
	if err != nil {
		ce.Handler.log.Warnfln("Failed to reply to command from %s: %v", ce.User.MXID, err)
	}
}

// Handle handles messages to the bridge
func (handler *CommandHandler) Handle(roomID id.RoomID, user *User, message string, replyTo id.EventID) {
	args := strings.Fields(message)
	if len(args) == 0 {
		args = []string{"unknown-command"}
	}
	ce := &CommandEvent{
		Bot:     handler.bridge.Bot,
		Bridge:  handler.bridge,
		Portal:  handler.bridge.GetPortalByMXID(roomID),
		Handler: handler,
		RoomID:  roomID,
		User:    user,
		Command: strings.ToLower(args[0]),
		Args:    args[1:],
		ReplyTo: replyTo,
	}
	handler.log.Debugfln("%s sent '%s' in %s", user.MXID, message, roomID)
	handler.CommandMux(ce)
}

func (handler *CommandHandler) CommandMux(ce *CommandEvent) {
	switch ce.Command {
	case "login":
		handler.CommandLogin(ce)
	case "ping-matrix":
		handler.CommandPingMatrix(ce)
	case "logout-matrix":
		handler.CommandLogoutMatrix(ce)
	case "help":
		handler.CommandHelp(ce)
	case "version":
		handler.CommandVersion(ce)
	case "reconnect", "connect":
		handler.CommandReconnect(ce)
	case "disconnect":
		handler.CommandDisconnect(ce)
	case "ping":
		handler.CommandPing(ce)
	case "delete-session":
		handler.CommandDeleteSession(ce)
	case "delete-portal":
		handler.CommandDeletePortal(ce)
	case "delete-all-portals":
		handler.CommandDeleteAllPortals(ce)
	case "discard-megolm-session", "discard-session":
		handler.CommandDiscardMegolmSession(ce)
	case "dev-test":
		handler.CommandDevTest(ce)
	case "set-pl":
		handler.CommandSetPowerLevel(ce)
	case "logout":
		handler.CommandLogout(ce)
	case "toggle":
		handler.CommandToggle(ce)
	case "set-relay", "unset-relay", "login-matrix", "sync", "list", "open", "pm", "invite-link", "check-invite", "join", "create", "accept":
		if !ce.User.HasSession() {
			ce.Reply("You are not logged in. Use the `login` command to log into WhatsApp.")
			return
		} else if !ce.User.IsLoggedIn() {
			ce.Reply("You are not connected to WhatsApp. Use the `reconnect` command to reconnect.")
			return
		}

		switch ce.Command {
		case "set-relay":
			handler.CommandSetRelay(ce)
		case "unset-relay":
			handler.CommandUnsetRelay(ce)
		case "login-matrix":
			handler.CommandLoginMatrix(ce)
		case "list":
			handler.CommandList(ce)
		case "open":
			handler.CommandOpen(ce)
		case "pm":
			handler.CommandPM(ce)
		case "invite-link":
			handler.CommandInviteLink(ce)
		case "check-invite":
			handler.CommandCheckInvite(ce)
		case "join":
			handler.CommandJoin(ce)
		case "create":
			handler.CommandCreate(ce)
		case "accept":
			handler.CommandAccept(ce)
		}
	default:
		ce.Reply("Unknown command, use the `help` command for help.")
	}
}

func (handler *CommandHandler) CommandDiscardMegolmSession(ce *CommandEvent) {
	if handler.bridge.Crypto == nil {
		ce.Reply("This bridge instance doesn't have end-to-bridge encryption enabled")
	} else if !ce.User.Admin {
		ce.Reply("Only the bridge admin can reset Megolm sessions")
	} else {
		handler.bridge.Crypto.ResetSession(ce.RoomID)
		ce.Reply("Successfully reset Megolm session in this room. New decryption keys will be shared the next time a message is sent from WhatsApp.")
	}
}

const cmdSetRelayHelp = `set-relay - Relay messages in this room through your WhatsApp account.`

func (handler *CommandHandler) CommandSetRelay(ce *CommandEvent) {
	if !handler.bridge.Config.Bridge.Relay.Enabled {
		ce.Reply("Relay mode is not enabled on this instance of the bridge")
	} else if ce.Portal == nil {
		ce.Reply("This is not a portal room")
	} else if handler.bridge.Config.Bridge.Relay.AdminOnly && !ce.User.Admin {
		ce.Reply("Only admins are allowed to enable relay mode on this instance of the bridge")
	} else {
		ce.Portal.RelayUserID = ce.User.MXID
		ce.Portal.Update()
		ce.Reply("Messages from non-logged-in users in this room will now be bridged through your WhatsApp account")
	}
}

const cmdUnsetRelayHelp = `unset-relay - Stop relaying messages in this room.`

func (handler *CommandHandler) CommandUnsetRelay(ce *CommandEvent) {
	if !handler.bridge.Config.Bridge.Relay.Enabled {
		ce.Reply("Relay mode is not enabled on this instance of the bridge")
	} else if ce.Portal == nil {
		ce.Reply("This is not a portal room")
	} else if handler.bridge.Config.Bridge.Relay.AdminOnly && !ce.User.Admin {
		ce.Reply("Only admins are allowed to enable relay mode on this instance of the bridge")
	} else {
		ce.Portal.RelayUserID = ""
		ce.Portal.Update()
		ce.Reply("Messages from non-logged-in users will no longer be bridged in this room")
	}
}

func (handler *CommandHandler) CommandDevTest(_ *CommandEvent) {

}

const cmdVersionHelp = `version - View the bridge version`

func (handler *CommandHandler) CommandVersion(ce *CommandEvent) {
	linkifiedVersion := fmt.Sprintf("v%s", Version)
	if Tag == Version {
		linkifiedVersion = fmt.Sprintf("[v%s](%s/releases/v%s)", Version, URL, Tag)
	} else if len(Commit) > 8 {
		linkifiedVersion = strings.Replace(linkifiedVersion, Commit[:8], fmt.Sprintf("[%s](%s/commit/%s)", Commit[:8], URL, Commit), 1)
	}
	ce.Reply(fmt.Sprintf("[%s](%s) %s (%s)", Name, URL, linkifiedVersion, BuildTime))
}

const cmdInviteLinkHelp = `invite-link [--reset] - Get an invite link to the current group chat, optionally regenerating the link and revoking the old link.`

func (handler *CommandHandler) CommandInviteLink(ce *CommandEvent) {
	reset := len(ce.Args) > 0 && strings.ToLower(ce.Args[0]) == "--reset"
	if ce.Portal == nil {
		ce.Reply("Not a portal room")
	} else if ce.Portal.IsPrivateChat() {
		ce.Reply("Can't get invite link to private chat")
	} else if ce.Portal.IsBroadcastList() {
		ce.Reply("Can't get invite link to broadcast list")
	} else if link, err := ce.User.Client.GetGroupInviteLink(ce.Portal.Key.JID, reset); err != nil {
		ce.Reply("Failed to get invite link: %v", err)
	} else {
		ce.Reply(link)
	}
}

const cmdCheckInviteHelp = `check-invite <invite link> - Resolve an invite link and check which group it points at.`
const inviteLinkPrefix = "https://chat.whatsapp.com/"

func (handler *CommandHandler) CommandCheckInvite(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `join <invite link>`")
		return
	} else if len(ce.Args[0]) <= len(inviteLinkPrefix) || ce.Args[0][:len(inviteLinkPrefix)] != inviteLinkPrefix {
		ce.Reply("That doesn't look like a WhatsApp invite link")
		return
	}
	group, err := ce.User.Client.GetGroupInfoFromLink(ce.Args[0])
	if err != nil {
		ce.Reply("Failed to get group info: %v", err)
		return
	}
	ce.Reply("That invite link points at %s (`%s`)", group.Name, group.JID)
}

const cmdJoinHelp = `join <invite link> - Join a group chat with an invite link.`

func (handler *CommandHandler) CommandJoin(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `join <invite link>`")
		return
	} else if len(ce.Args[0]) <= len(inviteLinkPrefix) || ce.Args[0][:len(inviteLinkPrefix)] != inviteLinkPrefix {
		ce.Reply("That doesn't look like a WhatsApp invite link")
		return
	}

	jid, err := ce.User.Client.JoinGroupViaLink(ce.Args[0])
	if err != nil {
		ce.Reply("Failed to join group: %v", err)
		return
	}
	handler.log.Debugln("%s successfully joined group %s", ce.User.MXID, jid)
	ce.Reply("Successfully joined group `%s`, the portal should be created momentarily", jid)
}

func (handler *CommandHandler) CommandAccept(ce *CommandEvent) {
	if ce.Portal == nil || len(ce.ReplyTo) == 0 {
		ce.Reply("You must reply to a group invite message when using this command.")
	} else if evt, err := ce.Portal.MainIntent().GetEvent(ce.RoomID, ce.ReplyTo); err != nil {
		handler.log.Errorln("Failed to get event %s to handle !wa accept command: %v", ce.ReplyTo, err)
		ce.Reply("Failed to get reply event")
	} else if meta, ok := evt.Content.Raw[inviteMetaField].(map[string]interface{}); !ok {
		ce.Reply("That doesn't look like a group invite message.")
	} else if jid, inviter, code, expiration, ok := parseInviteMeta(meta); !ok {
		ce.Reply("That doesn't look like a group invite message.")
	} else if inviter.User == ce.User.JID.User {
		ce.Reply("You can't accept your own invites")
	} else if err = ce.User.Client.AcceptGroupInvite(jid, inviter, code, expiration); err != nil {
		ce.Reply("Failed to accept group invite: %v", err)
	} else {
		ce.Reply("Successfully accepted the invite, the portal should be created momentarily")
	}
}

const cmdCreateHelp = `create - Create a group chat.`

func (handler *CommandHandler) CommandCreate(ce *CommandEvent) {
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

	participants := []types.JID{ce.User.JID.ToNonAD()}
	for userID := range members.Joined {
		jid, ok := handler.bridge.ParsePuppetMXID(userID)
		if ok && jid.User != ce.User.JID.User {
			participants = append(participants, jid)
		}
	}

	ce.Reply("Not yet implemented")
	// TODO reimplement
	//resp, err := ce.User.Conn.CreateGroup(roomNameEvent.Name, participants)
	//if err != nil {
	//	ce.Reply("Failed to create group: %v", err)
	//	return
	//}
	//portal := handler.bridge.GetPortalByJID(database.GroupPortalKey(resp.GroupID))
	//portal.roomCreateLock.Lock()
	//defer portal.roomCreateLock.Unlock()
	//if len(portal.MXID) != 0 {
	//	portal.log.Warnln("Detected race condition in room creation")
	//	// TODO race condition, clean up the old room
	//}
	//portal.MXID = ce.RoomID
	//portal.Name = roomNameEvent.Name
	//portal.Encrypted = encryptionEvent.Algorithm == id.AlgorithmMegolmV1
	//if !portal.Encrypted && handler.bridge.Config.Bridge.Encryption.Default {
	//	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateEncryption, "", &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1})
	//	if err != nil {
	//		portal.log.Warnln("Failed to enable e2be:", err)
	//	}
	//	portal.Encrypted = true
	//}
	//
	//portal.Update()
	//portal.UpdateBridgeInfo()
	//
	//ce.Reply("Successfully created WhatsApp group %s", portal.Key.JID)
	//inCommunity := ce.User.addPortalToCommunity(portal)
	//ce.User.CreateUserPortal(database.PortalKeyWithMeta{PortalKey: portal.Key, InCommunity: inCommunity})
}

func parseInviteMeta(meta map[string]interface{}) (jid, inviter types.JID, code string, expiration int64, ok bool) {
	var fieldFound bool
	code, fieldFound = meta["code"].(string)
	if !fieldFound {
		return
	}
	expirationStr, fieldFound := meta["expiration"].(string)
	if !fieldFound {
		return
	}
	inviterStr, fieldFound := meta["inviter"].(string)
	if !fieldFound {
		return
	}
	jidStr, fieldFound := meta["jid"].(string)
	if !fieldFound {
		return
	}
	var err error
	expiration, err = strconv.ParseInt(expirationStr, 10, 64)
	if err != nil {
		return
	}
	inviter, err = types.ParseJID(inviterStr)
	if err != nil {
		return
	}
	jid, err = types.ParseJID(jidStr)
	if err != nil {
		return
	}
	ok = true
	return
}

const cmdSetPowerLevelHelp = `set-pl [user ID] <power level> - Change the power level in a portal room. Only for bridge admins.`

func (handler *CommandHandler) CommandSetPowerLevel(ce *CommandEvent) {
	if ce.Portal == nil {
		ce.Reply("Not a portal room")
		return
	}
	var level int
	var userID id.UserID
	var err error
	if len(ce.Args) == 1 {
		level, err = strconv.Atoi(ce.Args[0])
		if err != nil {
			ce.Reply("Invalid power level \"%s\"", ce.Args[0])
			return
		}
		userID = ce.User.MXID
	} else if len(ce.Args) == 2 {
		userID = id.UserID(ce.Args[0])
		_, _, err := userID.Parse()
		if err != nil {
			ce.Reply("Invalid user ID \"%s\"", ce.Args[0])
			return
		}
		level, err = strconv.Atoi(ce.Args[1])
		if err != nil {
			ce.Reply("Invalid power level \"%s\"", ce.Args[1])
			return
		}
	} else {
		ce.Reply("**Usage:** `set-pl [user] <level>`")
		return
	}
	intent := ce.Portal.MainIntent()
	_, err = intent.SetPowerLevel(ce.RoomID, userID, level)
	if err != nil {
		ce.Reply("Failed to set power levels: %v", err)
	}
}

const cmdLoginHelp = `login - Link the bridge to your WhatsApp account as a web client`

// CommandLogin handles login command
func (handler *CommandHandler) CommandLogin(ce *CommandEvent) {
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
		switch item {
		case whatsmeow.QRChannelSuccess:
			jid := ce.User.Client.Store.ID
			ce.Reply("Successfully logged in as +%s (device #%d)", jid.User, jid.Device)
		case whatsmeow.QRChannelTimeout:
			ce.Reply("QR code timed out. Please restart the login.")
		case whatsmeow.QRChannelErrUnexpectedEvent:
			ce.Reply("Failed to log in: unexpected connection event from server")
		case whatsmeow.QRChannelScannedWithoutMultidevice:
			ce.Reply("Please enable the WhatsApp multidevice beta and scan the QR code again.")
		default:
			qrEventID = ce.User.sendQR(ce, string(item), qrEventID)
		}
	}
	_, _ = ce.Bot.RedactEvent(ce.RoomID, qrEventID)
}

func (user *User) sendQR(ce *CommandEvent, code string, prevEvent id.EventID) id.EventID {
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

func (user *User) uploadQR(ce *CommandEvent, code string) (id.ContentURI, bool) {
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

const cmdLogoutHelp = `logout - Unlink the bridge from your WhatsApp account`

// CommandLogout handles !logout command
func (handler *CommandHandler) CommandLogout(ce *CommandEvent) {
	if ce.User.Session == nil {
		ce.Reply("You're not logged in.")
		return
	} else if !ce.User.IsLoggedIn() {
		ce.Reply("You are not connected to WhatsApp. Use the `reconnect` command to reconnect, or `delete-session` to forget all login information.")
		return
	}
	puppet := handler.bridge.GetPuppetByJID(ce.User.JID)
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
	ce.User.removeFromJIDMap(StateLoggedOut)
	ce.User.DeleteConnection()
	ce.User.DeleteSession()
	ce.Reply("Logged out successfully.")
}

const cmdToggleHelp = `toggle <presence|receipts|all> - Toggle bridging of presence or read receipts`

func (handler *CommandHandler) CommandToggle(ce *CommandEvent) {
	if len(ce.Args) == 0 || (ce.Args[0] != "presence" && ce.Args[0] != "receipts" && ce.Args[0] != "all") {
		ce.Reply("**Usage:** `toggle <presence|receipts|all>`")
		return
	}
	if ce.User.Session == nil {
		ce.Reply("You're not logged in.")
		return
	}
	customPuppet := handler.bridge.GetPuppetByCustomMXID(ce.User.MXID)
	if customPuppet == nil {
		ce.Reply("You're not logged in with your Matrix account.")
		return
	}
	if ce.Args[0] == "presence" || ce.Args[0] == "all" {
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
	}
	if ce.Args[0] == "receipts" || ce.Args[0] == "all" {
		customPuppet.EnableReceipts = !customPuppet.EnableReceipts
		if customPuppet.EnableReceipts {
			ce.Reply("Enabled read receipt bridging")
		} else {
			ce.Reply("Disabled read receipt bridging")
		}
	}
	customPuppet.Update()
}

const cmdDeleteSessionHelp = `delete-session - Delete session information and disconnect from WhatsApp without sending a logout request`

func (handler *CommandHandler) CommandDeleteSession(ce *CommandEvent) {
	if ce.User.Session == nil && ce.User.Client == nil {
		ce.Reply("Nothing to purge: no session information stored and no active connection.")
		return
	}
	ce.User.removeFromJIDMap(StateLoggedOut)
	ce.User.DeleteConnection()
	ce.User.DeleteSession()
	ce.Reply("Session information purged")
}

const cmdReconnectHelp = `reconnect - Reconnect to WhatsApp`

func (handler *CommandHandler) CommandReconnect(ce *CommandEvent) {
	if ce.User.Client == nil {
		if ce.User.Session == nil {
			ce.Reply("You're not logged into WhatsApp. Please log in first.")
		} else {
			ce.User.Connect()
			ce.Reply("Started connecting to WhatsApp")
		}
	} else {
		ce.User.DeleteConnection()
		ce.User.sendBridgeState(BridgeState{StateEvent: StateTransientDisconnect, Error: WANotConnected})
		ce.User.Connect()
		ce.Reply("Restarted connection to WhatsApp")
	}
}

const cmdDisconnectHelp = `disconnect - Disconnect from WhatsApp (without logging out)`

func (handler *CommandHandler) CommandDisconnect(ce *CommandEvent) {
	if ce.User.Client == nil {
		ce.Reply("You don't have a WhatsApp connection.")
		return
	}
	ce.User.DeleteConnection()
	ce.Reply("Successfully disconnected. Use the `reconnect` command to reconnect.")
	ce.User.sendBridgeState(BridgeState{StateEvent: StateBadCredentials, Error: WANotConnected})
}

const cmdPingHelp = `ping - Check your connection to WhatsApp.`

func (handler *CommandHandler) CommandPing(ce *CommandEvent) {
	if ce.User.Session == nil {
		ce.Reply("You're not logged into WhatsApp.")
	} else if ce.User.Client == nil || !ce.User.Client.IsConnected() {
		ce.Reply("You don't have a WhatsApp connection.")
	} else {
		ce.Reply("Connection to WhatsApp OK (probably)")
	}
}

const cmdHelpHelp = `help - Prints this help`

// CommandHelp handles help command
func (handler *CommandHandler) CommandHelp(ce *CommandEvent) {
	cmdPrefix := ""
	if ce.User.ManagementRoom != ce.RoomID {
		cmdPrefix = handler.bridge.Config.Bridge.CommandPrefix + " "
	}

	ce.Reply("* " + strings.Join([]string{
		cmdPrefix + cmdHelpHelp,
		cmdPrefix + cmdVersionHelp,
		cmdPrefix + cmdLoginHelp,
		cmdPrefix + cmdLogoutHelp,
		cmdPrefix + cmdDeleteSessionHelp,
		cmdPrefix + cmdReconnectHelp,
		cmdPrefix + cmdDisconnectHelp,
		cmdPrefix + cmdPingHelp,
		cmdPrefix + cmdSetRelayHelp,
		cmdPrefix + cmdUnsetRelayHelp,
		cmdPrefix + cmdLoginMatrixHelp,
		cmdPrefix + cmdPingMatrixHelp,
		cmdPrefix + cmdLogoutMatrixHelp,
		cmdPrefix + cmdToggleHelp,
		cmdPrefix + cmdListHelp,
		cmdPrefix + cmdOpenHelp,
		cmdPrefix + cmdPMHelp,
		cmdPrefix + cmdInviteLinkHelp,
		cmdPrefix + cmdCheckInviteHelp,
		cmdPrefix + cmdJoinHelp,
		cmdPrefix + cmdCreateHelp,
		cmdPrefix + cmdSetPowerLevelHelp,
		cmdPrefix + cmdDeletePortalHelp,
		cmdPrefix + cmdDeleteAllPortalsHelp,
	}, "\n* "))
}

func canDeletePortal(portal *Portal, userID id.UserID) bool {
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

const cmdDeletePortalHelp = `delete-portal - Delete the current portal. If the portal is used by other people, this is limited to bridge admins.`

func (handler *CommandHandler) CommandDeletePortal(ce *CommandEvent) {
	if ce.Portal == nil {
		ce.Reply("You must be in a portal room to use that command")
		return
	}

	if !ce.User.Admin && !canDeletePortal(ce.Portal, ce.User.MXID) {
		ce.Reply("Only bridge admins can delete portals with other Matrix users")
		return
	}

	ce.Portal.log.Infoln(ce.User.MXID, "requested deletion of portal.")
	ce.Portal.Delete()
	ce.Portal.Cleanup(false)
}

const cmdDeleteAllPortalsHelp = `delete-all-portals - Delete all portals.`

func (handler *CommandHandler) CommandDeleteAllPortals(ce *CommandEvent) {
	portals := handler.bridge.GetAllPortals()
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

	leave := func(portal *Portal) {
		if len(portal.MXID) > 0 {
			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{
				Reason: "Deleting portal",
				UserID: ce.User.MXID,
			})
		}
	}
	customPuppet := handler.bridge.GetPuppetByCustomMXID(ce.User.MXID)
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

const cmdListHelp = `list <contacts|groups> [page] [items per page] - Get a list of all contacts and groups.`

func formatContacts(bridge *Bridge, input map[types.JID]types.ContactInfo) (result []string) {
	for jid, contact := range input {
		if len(contact.FullName) == 0 {
			continue
		}
		puppet := bridge.GetPuppetByJID(jid)
		pushName := contact.PushName
		if len(pushName) == 0 {
			pushName = contact.FullName
		}
		result = append(result, fmt.Sprintf("* %s / [%s](https://matrix.to/#/%s) - `+%s`", contact.FullName, pushName, puppet.MXID, jid.User))
	}
	sort.Sort(sort.StringSlice(result))
	return
}

func formatGroups(input []*types.GroupInfo) (result []string) {
	for _, group := range input {
		result = append(result, fmt.Sprintf("* %s - `%s`", group.GroupName.Name, group.JID.User))
	}
	sort.Sort(sort.StringSlice(result))
	return
}

func (handler *CommandHandler) CommandList(ce *CommandEvent) {
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
		result = formatContacts(ce.User.bridge, contactList)

	} else {
		groupList, err := ce.User.Client.GetJoinedGroups()
		if err != nil {
			ce.Reply("Failed to get groups: %s", err)
			return
		}
		result = formatGroups(groupList)
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
			ce.Reply("There are only %d pages of %s", pages, strings.ToLower(typeName))
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

const cmdOpenHelp = `open <_group JID_> - Open a group chat portal.`

func (handler *CommandHandler) CommandOpen(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `open <group JID>`")
		return
	}
	ce.Reply("Not yet implemented")

	// TODO reimplement
	//user := ce.User
	//jid := ce.Args[0]
	//if strings.HasSuffix(jid, whatsapp.NewUserSuffix) {
	//	ce.Reply("That looks like a user JID. Did you mean `pm %s`?", jid[:len(jid)-len(whatsapp.NewUserSuffix)])
	//	return
	//}
	//
	//user.Conn.Store.ContactsLock.RLock()
	//contact, ok := user.Conn.Store.Contacts[jid]
	//user.Conn.Store.ContactsLock.RUnlock()
	//if !ok {
	//	ce.Reply("Group JID not found in contacts. Try syncing contacts with `sync` first.")
	//	return
	//}
	//handler.log.Debugln("Importing", jid, "for", user)
	//portal := user.bridge.GetPortalByJID(database.GroupPortalKey(jid))
	//if len(portal.MXID) > 0 {
	//	portal.Sync(user, contact)
	//	ce.Reply("Portal room synced.")
	//} else {
	//	portal.Sync(user, contact)
	//	ce.Reply("Portal room created.")
	//}
	//_, _ = portal.MainIntent().InviteUser(portal.MXID, &mautrix.ReqInviteUser{UserID: user.MXID})
}

const cmdPMHelp = `pm <_international phone number_> - Open a private chat with the given phone number.`

func (handler *CommandHandler) CommandPM(ce *CommandEvent) {
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

	handler.log.Debugln("Importing", targetUser.JID, "for", user)
	puppet := user.bridge.GetPuppetByJID(targetUser.JID)
	puppet.SyncContact(user, true)
	portal := user.GetPortalByJID(puppet.JID)
	if len(portal.MXID) > 0 {
		ok := portal.ensureUserInvited(user)
		if !ok {
			portal.log.Warnfln("ensureUserInvited(%s) returned false, creating new portal", user.MXID)
			portal.MXID = ""
		} else {
			ce.Reply("You already have a private chat portal with that user at [%s](https://matrix.to/#/%s)", puppet.Displayname, portal.MXID)
			return
		}
	}
	err = portal.CreateMatrixRoom(user, nil)
	if err != nil {
		ce.Reply("Failed to create portal room: %v", err)
		return
	}
	ce.Reply("Created portal room and invited you to it.")
}

const cmdLoginMatrixHelp = `login-matrix <_access token_> - Replace your WhatsApp account's Matrix puppet with your real Matrix account.`

func (handler *CommandHandler) CommandLoginMatrix(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `login-matrix <access token>`")
		return
	}
	puppet := handler.bridge.GetPuppetByJID(ce.User.JID)
	err := puppet.SwitchCustomMXID(ce.Args[0], ce.User.MXID)
	if err != nil {
		ce.Reply("Failed to switch puppet: %v", err)
		return
	}
	ce.Reply("Successfully switched puppet")
}

const cmdPingMatrixHelp = `ping-matrix - Check if your double puppet is working correctly.`

func (handler *CommandHandler) CommandPingMatrix(ce *CommandEvent) {
	puppet := handler.bridge.GetPuppetByCustomMXID(ce.User.MXID)
	if puppet == nil || puppet.CustomIntent() == nil {
		ce.Reply("You have not changed your WhatsApp account's Matrix puppet.")
		return
	}
	resp, err := puppet.CustomIntent().Whoami()
	if err != nil {
		ce.Reply("Failed to validate Matrix login: %v", err)
	} else {
		ce.Reply("Confirmed valid access token for %s / %s", resp.UserID, resp.DeviceID)
	}
}

const cmdLogoutMatrixHelp = `logout-matrix - Switch your WhatsApp account's Matrix puppet back to the default one.`

func (handler *CommandHandler) CommandLogoutMatrix(ce *CommandEvent) {
	puppet := handler.bridge.GetPuppetByCustomMXID(ce.User.MXID)
	if puppet == nil || puppet.CustomIntent() == nil {
		ce.Reply("You had not changed your WhatsApp account's Matrix puppet.")
		return
	}
	err := puppet.SwitchCustomMXID("", "")
	if err != nil {
		ce.Reply("Failed to remove custom puppet: %v", err)
		return
	}
	ce.Reply("Successfully removed custom puppet")
}
