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
	"time"

	"github.com/Rhymen/go-whatsapp"

	"maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix/format"

	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
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
	Handler *CommandHandler
	RoomID  types.MatrixRoomID
	User    *User
	Command string
	Args    []string
}

// Reply sends a reply to command as notice
func (ce *CommandEvent) Reply(msg string, args ...interface{}) {
	content := format.RenderMarkdown(fmt.Sprintf(msg, args...))
	content.MsgType = mautrix.MsgNotice
	room := ce.User.ManagementRoom
	if len(room) == 0 {
		room = ce.RoomID
	}
	_, err := ce.Bot.SendMessageEvent(room, mautrix.EventMessage, content)
	if err != nil {
		ce.Handler.log.Warnfln("Failed to reply to command from %s: %v", ce.User.MXID, err)
	}
}

// Handle handles messages to the bridge
func (handler *CommandHandler) Handle(roomID types.MatrixRoomID, user *User, message string) {
	args := strings.Fields(message)
	ce := &CommandEvent{
		Bot:     handler.bridge.Bot,
		Bridge:  handler.bridge,
		Handler: handler,
		RoomID:  roomID,
		User:    user,
		Command: strings.ToLower(args[0]),
		Args:    args[1:],
	}
	handler.log.Debugfln("%s sent '%s' in %s", user.MXID, message, roomID)
	if roomID == handler.bridge.Config.Bridge.Relaybot.ManagementRoom {
		handler.CommandRelaybot(ce)
	} else {
		handler.CommandMux(ce)
	}
}

func (handler *CommandHandler) CommandMux(ce *CommandEvent) {
	switch ce.Command {
	case "relaybot":
		handler.CommandRelaybot(ce)
	case "login":
		handler.CommandLogin(ce)
	case "logout-matrix":
		handler.CommandLogoutMatrix(ce)
	case "help":
		handler.CommandHelp(ce)
	case "reconnect":
		handler.CommandReconnect(ce)
	case "disconnect":
		handler.CommandDisconnect(ce)
	case "ping":
		handler.CommandPing(ce)
	case "delete-connection":
		handler.CommandDeleteConnection(ce)
	case "delete-session":
		handler.CommandDeleteSession(ce)
	case "delete-portal":
		handler.CommandDeletePortal(ce)
	case "delete-all-portals":
		handler.CommandDeleteAllPortals(ce)
	case "dev-test":
		handler.CommandDevTest(ce)
	case "login-matrix", "logout", "sync", "list", "open", "pm", "invite", "kick", "leave", "join", "create":
		if !ce.User.HasSession() {
			ce.Reply("You are not logged in. Use the `login` command to log into WhatsApp.")
			return
		} else if !ce.User.IsConnected() {
			ce.Reply("You are not connected to WhatsApp. Use the `reconnect` command to reconnect.")
			return
		}

		switch ce.Command {
		case "login-matrix":
			handler.CommandLoginMatrix(ce)
		case "logout":
			handler.CommandLogout(ce)
		case "sync":
			handler.CommandSync(ce)
		case "list":
			handler.CommandList(ce)
		case "open":
			handler.CommandOpen(ce)
		case "pm":
			handler.CommandPM(ce)
		case "invite":
			handler.CommandInvite(ce)
		case "kick":
			handler.CommandKick(ce)
		case "leave":
			handler.CommandLeave(ce)
		case "join":
			handler.CommandJoin(ce)
		case "create":
			handler.CommandCreate(ce)
		}
	default:
		ce.Reply("Unknown Command")
	}
}

func (handler *CommandHandler) CommandRelaybot(ce *CommandEvent) {
	if handler.bridge.Relaybot == nil {
		ce.Reply("The relaybot is disabled")
	} else if !ce.User.Admin {
		ce.Reply("Only admins can manage the relaybot")
	} else {
		if ce.Command == "relaybot" {
			if len(ce.Args) == 0 {
				ce.Reply("**Usage:** `relaybot <command>`")
				return
			}
			ce.Command = strings.ToLower(ce.Args[0])
			ce.Args = ce.Args[1:]
		}
		ce.User = handler.bridge.Relaybot
		handler.CommandMux(ce)
	}
}

func (handler *CommandHandler) CommandDevTest(ce *CommandEvent) {

}

const cmdLoginHelp = `login - Authenticate this Bridge as WhatsApp Web Client`

// CommandLogin handles login command
func (handler *CommandHandler) CommandLogin(ce *CommandEvent) {
	if !ce.User.Connect(true) {
		ce.User.log.Debugln("Connect() returned false, assuming error was logged elsewhere and canceling login.")
		return
	}
	ce.User.Login(ce)
}

const cmdLogoutHelp = `logout - Logout from WhatsApp`

// CommandLogout handles !logout command
func (handler *CommandHandler) CommandLogout(ce *CommandEvent) {
	if ce.User.Session == nil {
		ce.Reply("You're not logged in.")
		return
	}
	err := ce.User.Conn.Logout()
	if err != nil {
		ce.User.log.Warnln("Error while logging out:", err)
		ce.Reply("Unknown error while logging out: %v", err)
		return
	}
	_, err = ce.User.Conn.Disconnect()
	if err != nil {
		ce.User.log.Warnln("Error while disconnecting after logout:", err)
	}
	ce.User.Conn.RemoveHandlers()
	ce.User.Conn = nil
	ce.User.SetSession(nil)
	ce.Reply("Logged out successfully.")
}

const cmdDeleteSessionHelp = `delete-session - Delete session information and disconnect from WhatsApp without sending a logout request`

func (handler *CommandHandler) CommandDeleteSession(ce *CommandEvent) {
	if ce.User.Session == nil && ce.User.Conn == nil {
		ce.Reply("Nothing to purge: no session information stored and no active connection.")
		return
	}
	ce.User.SetSession(nil)
	if ce.User.Conn != nil {
		_, _ = ce.User.Conn.Disconnect()
		ce.User.Conn.RemoveHandlers()
		ce.User.Conn = nil
	}
	ce.Reply("Session information purged")
}

const cmdReconnectHelp = `reconnect - Reconnect to WhatsApp`

func (handler *CommandHandler) CommandReconnect(ce *CommandEvent) {
	if ce.User.Conn == nil {
		if ce.User.Session == nil {
			ce.Reply("No existing connection and no session. Did you mean `login`?")
		} else {
			ce.Reply("No existing connection, creating one...")
			ce.User.Connect(false)
		}
		return
	}

	wasConnected := true
	sess, err := ce.User.Conn.Disconnect()
	if err == whatsapp.ErrNotConnected {
		wasConnected = false
	} else if err != nil {
		ce.User.log.Warnln("Error while disconnecting:", err)
	} else if len(sess.Wid) > 0 {
		ce.User.SetSession(&sess)
	}

	err = ce.User.Conn.Restore()
	if err == whatsapp.ErrInvalidSession {
		if ce.User.Session != nil {
			ce.User.log.Debugln("Got invalid session error when reconnecting, but user has session. Retrying using RestoreWithSession()...")
			var sess whatsapp.Session
			sess, err = ce.User.Conn.RestoreWithSession(*ce.User.Session)
			if err == nil {
				ce.User.SetSession(&sess)
			}
		} else {
			ce.Reply("You are not logged in.")
			return
		}
	} else if err == whatsapp.ErrLoginInProgress {
		ce.Reply("A login or reconnection is already in progress.")
		return
	} else if err == whatsapp.ErrAlreadyLoggedIn {
		ce.Reply("You were already connected.")
		return
	}
	if err != nil {
		ce.User.log.Warnln("Error while reconnecting:", err)
		if err.Error() == "restore session connection timed out" {
			ce.Reply("Reconnection timed out. Is WhatsApp on your phone reachable?")
		} else {
			ce.Reply("Unknown error while reconnecting: %v", err)
		}
		ce.User.log.Debugln("Disconnecting due to failed session restore in reconnect command...")
		sess, err := ce.User.Conn.Disconnect()
		if err != nil {
			ce.User.log.Errorln("Failed to disconnect after failed session restore in reconnect command:", err)
		} else if len(sess.Wid) > 0 {
			ce.User.SetSession(&sess)
		}
		return
	}
	ce.User.ConnectionErrors = 0

	var msg string
	if wasConnected {
		msg = "Reconnected successfully."
	} else {
		msg = "Connected successfully."
	}
	ce.Reply(msg)
	ce.User.PostLogin()
}

func (handler *CommandHandler) CommandDeleteConnection(ce *CommandEvent) {
	if ce.User.Conn == nil {
		ce.Reply("You don't have a WhatsApp connection.")
		return
	}
	sess, err := ce.User.Conn.Disconnect()
	if err == nil && len(sess.Wid) > 0 {
		ce.User.SetSession(&sess)
	}
	ce.User.Conn.RemoveHandlers()
	ce.User.Conn = nil
	ce.Reply("Successfully disconnected. Use the `reconnect` command to reconnect.")
}

const cmdDisconnectHelp = `disconnect - Disconnect from WhatsApp (without logging out)`

func (handler *CommandHandler) CommandDisconnect(ce *CommandEvent) {
	if ce.User.Conn == nil {
		ce.Reply("You don't have a WhatsApp connection.")
		return
	}
	sess, err := ce.User.Conn.Disconnect()
	if err == whatsapp.ErrNotConnected {
		ce.Reply("You were not connected.")
		return
	} else if err != nil {
		ce.User.log.Warnln("Error while disconnecting:", err)
		ce.Reply("Unknown error while disconnecting: %v", err)
		return
	} else if len(sess.Wid) > 0 {
		ce.User.SetSession(&sess)
	}
	ce.Reply("Successfully disconnected. Use the `reconnect` command to reconnect.")
}

const cmdPingHelp = `ping - Check your connection to WhatsApp.`

func (handler *CommandHandler) CommandPing(ce *CommandEvent) {
	if ce.User.Session == nil {
		if ce.User.IsLoginInProgress() {
			ce.Reply("You're not logged into WhatsApp, but there's a login in progress.")
		} else {
			ce.Reply("You're not logged into WhatsApp.")
		}
	} else if ce.User.Conn == nil {
		ce.Reply("You don't have a WhatsApp connection.")
	} else if ok, err := ce.User.Conn.AdminTest(); err != nil {
		if ce.User.IsLoginInProgress() {
			ce.Reply("Connection not OK: %v, but login in progress", err)
		} else {
			ce.Reply("Connection not OK: %v", err)
		}
	} else if !ok {
		if ce.User.IsLoginInProgress() {
			ce.Reply("Connection not OK, but no error received and login in progress")
		} else {
			ce.Reply("Connection not OK, but no error received")
		}
	} else {
		ce.Reply("Connection to WhatsApp OK")
	}
}

const cmdHelpHelp = `help - Prints this help`

// CommandHelp handles help command
func (handler *CommandHandler) CommandHelp(ce *CommandEvent) {
	cmdPrefix := ""
	if ce.User.ManagementRoom != ce.RoomID || ce.User.IsRelaybot {
		cmdPrefix = handler.bridge.Config.Bridge.CommandPrefix + " "
	}

	ce.Reply("* " + strings.Join([]string{
		cmdPrefix + cmdHelpHelp,
		cmdPrefix + cmdLoginHelp,
		cmdPrefix + cmdLogoutHelp,
		cmdPrefix + cmdDeleteSessionHelp,
		cmdPrefix + cmdReconnectHelp,
		cmdPrefix + cmdDisconnectHelp,
		cmdPrefix + cmdPingHelp,
		cmdPrefix + cmdLoginMatrixHelp,
		cmdPrefix + cmdLogoutMatrixHelp,
		cmdPrefix + cmdSyncHelp,
		cmdPrefix + cmdListHelp,
		cmdPrefix + cmdOpenHelp,
		cmdPrefix + cmdPMHelp,
		cmdPrefix + cmdCreateHelp,
		cmdPrefix + cmdInviteHelp,
		cmdPrefix + cmdKickHelp,
		cmdPrefix + cmdLeaveHelp,
		cmdPrefix + cmdJoinHelp,
	}, "\n* "))
}

const cmdSyncHelp = `sync [--create-all] - Synchronize contacts from phone and optionally create portals for group chats.`

// CommandSync handles sync command
func (handler *CommandHandler) CommandSync(ce *CommandEvent) {
	user := ce.User
	create := len(ce.Args) > 0 && ce.Args[0] == "--create-all"

	ce.Reply("Updating contact and chat list...")
	handler.log.Debugln("Importing contacts of", user.MXID)
	_, err := user.Conn.Contacts()
	if err != nil {
		user.log.Errorln("Error updating contacts:", err)
		ce.Reply("Failed to sync contact list (see logs for details)")
		return
	}
	handler.log.Debugln("Importing chats of", user.MXID)
	_, err = user.Conn.Chats()
	if err != nil {
		user.log.Errorln("Error updating chats:", err)
		ce.Reply("Failed to sync chat list (see logs for details)")
		return
	}

	ce.Reply("Syncing contacts...")
	user.syncPuppets(nil)
	ce.Reply("Syncing chats...")
	user.syncPortals(nil, create)

	ce.Reply("Sync complete.")
}

func (handler *CommandHandler) CommandDeletePortal(ce *CommandEvent) {
	if !ce.User.Admin {
		ce.Reply("Only bridge admins can delete portals")
		return
	}

	portal := ce.Bridge.GetPortalByMXID(ce.RoomID)
	if portal == nil {
		ce.Reply("You must be in a portal room to use that command")
		return
	}

	portal.log.Infoln(ce.User.MXID, "requested deletion of portal.")
	portal.Delete()
	portal.Cleanup(false)
}

func (handler *CommandHandler) CommandDeleteAllPortals(ce *CommandEvent) {
	portals := ce.User.GetPortals()
	portalsToDelete := make([]*Portal, 0, len(portals))
	for _, portal := range portals {
		users := portal.GetUserIDs()
		if len(users) == 1 && users[0] == ce.User.MXID {
			portalsToDelete = append(portalsToDelete, portal)
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
	ce.Reply("Found %d portals with no other users, deleting...", len(portalsToDelete))
	for _, portal := range portalsToDelete {
		portal.Delete()
		leave(portal)
	}
	ce.Reply("Finished deleting portal info. Now cleaning up rooms in background. " +
		"You may already continue using the bridge. Use `sync` to recreate portals.")

	go func() {
		for _, portal := range portalsToDelete {
			portal.Cleanup(false)
		}
		ce.Reply("Finished background cleanup of deleted portal rooms.")
	}()
}

const cmdListHelp = `list - Get a list of all contacts and groups.`

func (handler *CommandHandler) CommandList(ce *CommandEvent) {
	var contacts strings.Builder
	var groups strings.Builder

	for jid, contact := range ce.User.Conn.Store.Contacts {
		if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
			_, _ = fmt.Fprintf(&contacts, "* %s / %s - `%s`\n", contact.Name, contact.Notify, contact.Jid[:len(contact.Jid)-len(whatsappExt.NewUserSuffix)])
		} else {
			_, _ = fmt.Fprintf(&groups, "* %s - `%s`\n", contact.Name, contact.Jid)
		}
	}
	ce.Reply("### Contacts\n%s\n\n### Groups\n%s", contacts.String(), groups.String())
}

const cmdOpenHelp = `open <_group JID_> - Open a group chat portal.`

func (handler *CommandHandler) CommandOpen(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `open <group JID>`")
		return
	}

	user := ce.User
	jid := ce.Args[0]

	if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
		ce.Reply("That looks like a user JID. Did you mean `pm %s`?", jid[:len(jid)-len(whatsappExt.NewUserSuffix)])
		return
	}

	contact, ok := user.Conn.Store.Contacts[jid]
	if !ok {
		ce.Reply("Group JID not found in contacts. Try syncing contacts with `sync` first.")
		return
	}
	handler.log.Debugln("Importing", jid, "for", user)
	portal := user.bridge.GetPortalByJID(database.GroupPortalKey(jid))
	if len(portal.MXID) > 0 {
		portal.Sync(user, contact)
		ce.Reply("Portal room synced.")
	} else {
		portal.Sync(user, contact)
		ce.Reply("Portal room created.")
	}
}

const cmdPMHelp = `pm [--force] <_international phone number_> - Open a private chat with the given phone number.`

func (handler *CommandHandler) CommandPM(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `pm [--force] <international phone number>`")
		return
	}

	force := ce.Args[0] == "--force"
	if force {
		ce.Args = ce.Args[1:]
	}

	user := ce.User

	number := strings.Join(ce.Args, "")
	if number[0] == '+' {
		number = number[1:]
	}
	for _, char := range number {
		if char < '0' || char > '9' {
			ce.Reply("Invalid phone number.")
			return
		}
	}
	jid := number + whatsappExt.NewUserSuffix

	handler.log.Debugln("Importing", jid, "for", user)

	contact, ok := user.Conn.Store.Contacts[jid]
	if !ok {
		if !force {
			ce.Reply("Phone number not found in contacts. Try syncing contacts with `sync` first. " +
				"To create a portal anyway, use `pm --force <number>`.")
			return
		}
		contact = whatsapp.Contact{Jid: jid}
	}
	puppet := user.bridge.GetPuppetByJID(contact.Jid)
	puppet.Sync(user, contact)
	portal := user.bridge.GetPortalByJID(database.NewPortalKey(contact.Jid, user.JID))
	if len(portal.MXID) > 0 {
		_, err := portal.MainIntent().InviteUser(portal.MXID, &mautrix.ReqInviteUser{UserID: user.MXID})
		if err != nil {
			fmt.Println(err)
		} else {
			ce.Reply("Existing portal room found, invited you to it.")
		}
		return
	}
	err := portal.CreateMatrixRoom(user)
	if err != nil {
		ce.Reply("Failed to create portal room: %v", err)
		return
	}
	ce.Reply("Created portal room and invited you to it.")
}

const cmdLoginMatrixHelp = `login-matrix <_access token_> - Replace your WhatsApp account's Matrix puppet with your real Matrix account.'`

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

const cmdLogoutMatrixHelp = `logout-matrix - Switch your WhatsApp account's Matrix puppet back to the default one.`

func (handler *CommandHandler) CommandLogoutMatrix(ce *CommandEvent) {
	puppet := handler.bridge.GetPuppetByJID(ce.User.JID)
	if len(puppet.CustomMXID) == 0 {
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

const cmdInviteHelp = `invite <_group JID_> <_international phone number_>,... - Invite members to a group.`

func (handler *CommandHandler) CommandInvite(ce *CommandEvent) {
	if len(ce.Args) < 2 {
		ce.Reply("**Usage:** `invite <group JID> <international phone number>,...`")
		return
	}

	user := ce.User
	jid := ce.Args[0]
	userNumbers := strings.Split(ce.Args[1], ",")

	if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
		ce.Reply("**Usage:** `invite <group JID> <international phone number>,...`")
		return
	}

	for i, number := range userNumbers {
		userNumbers[i] = number + whatsappExt.NewUserSuffix
	}

	contact, ok := user.Conn.Store.Contacts[jid]
	if !ok {
		ce.Reply("Group JID not found in contacts. Try syncing contacts with `sync` first.")
		return
	}
	handler.log.Debugln("Importing", jid, "for", user)
	portal := user.bridge.GetPortalByJID(database.GroupPortalKey(jid))
	if len(portal.MXID) > 0 {
		portal.Sync(user, contact)
		ce.Reply("Portal room synced.")
	} else {
		portal.Sync(user, contact)
		ce.Reply("Portal room created.")
	}

	handler.log.Debugln("Inviting", userNumbers, "to", jid)
	err := user.Conn.HandleGroupInvite(jid, userNumbers)
	if err != nil {
		ce.Reply("Please confirm that you have permission to invite members.")
	} else {
		ce.Reply("Group invitation sent.\nIf the member fails to join the group, please check your permissions or command parameters")
	}
	time.Sleep(time.Duration(3)*time.Second)
	ce.Reply("Syncing room puppet...")
	chatMap := make(map[string]whatsapp.Chat)
	for _, chat := range user.Conn.Store.Chats {
		if chat.Jid == jid {
			chatMap[chat.Jid]= chat
		}
	}
	user.syncPortals(chatMap, false)
	ce.Reply("Syncing room puppet completed")
}

const cmdKickHelp = `kick <_group JID_> <_international phone number_>,... <_reason_> - Remove members from the group.`

func (handler *CommandHandler) CommandKick(ce *CommandEvent) {
	if len(ce.Args) < 2 {
		ce.Reply("**Usage:** `kick <group JID> <international phone number>,... reason`")
		return
	}

	user := ce.User
	jid := ce.Args[0]
	userNumbers := strings.Split(ce.Args[1], ",")
	reason := "omitempty"
	if len(ce.Args) > 2 {
		reason = ce.Args[0]
	}

	if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
		ce.Reply("**Usage:** `kick <group JID> <international phone number>,... reason`")
		return
	}

	contact, ok := user.Conn.Store.Contacts[jid]
	if !ok {
		ce.Reply("Group JID not found in contacts. Try syncing contacts with `sync` first.")
		return
	}
	handler.log.Debugln("Importing", jid, "for", user)
	portal := user.bridge.GetPortalByJID(database.GroupPortalKey(jid))
	if len(portal.MXID) > 0 {
		portal.Sync(user, contact)
		ce.Reply("Portal room synced.")
	} else {
		portal.Sync(user, contact)
		ce.Reply("Portal room created.")
	}

	for i, number := range userNumbers {
		userNumbers[i] = number + whatsappExt.NewUserSuffix
		member := portal.bridge.GetPuppetByJID(number + whatsappExt.NewUserSuffix)
		if member == nil {
			portal.log.Errorln("%s is not a puppet", number)
			return
		}
		_, err := portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{
			Reason: reason,
			UserID: member.MXID,
		})
		if err != nil {
			portal.log.Errorln("Error kicking user while command kick:", err)
		}
	}

	handler.log.Debugln("Kicking", userNumbers, "to", jid)
	err := user.Conn.HandleGroupKick(jid, userNumbers)
	if err != nil {
		ce.Reply("Please confirm that you have permission to kick members.")
	} else {
		ce.Reply("Remove operation completed.\nIf the member has not been removed, please check your permissions or command parameters")
	}
}

const cmdLeaveHelp = `leave <_group JID_> - leave a group.`

func (handler *CommandHandler) CommandLeave(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `leave <group JID>`")
		return
	}

	user := ce.User
	jid := ce.Args[0]

	if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
		ce.Reply("**Usage:** `leave <group JID>`")
		return
	}

	err := user.Conn.HandleGroupLeave(jid)
	if err == nil {
		ce.Reply("Leave operation completed.")
	}

	handler.log.Debugln("Importing", jid, "for", user)
	portal := user.bridge.GetPortalByJID(database.GroupPortalKey(jid))
	if len(portal.MXID) > 0 {
		_, errLeave := portal.MainIntent().LeaveRoom(portal.MXID)
		if errLeave != nil {
			portal.log.Errorln("Error leaving matrix room:", err)
		}
	}
}

const cmdJoinHelp = `join <_Invitation link|code_> - Join the group via the invitation link.`

func (handler *CommandHandler) CommandJoin(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `join <Invitation link||code>`")
		return
	}

	user := ce.User
	params := strings.Split(ce.Args[0], "com/")

	jid, err := user.Conn.HandleGroupJoin(params[len(params)-1])
	if err == nil {
		ce.Reply("Join operation completed.")
	}

	contact, ok := user.Conn.Store.Contacts[jid]
	if !ok {
		ce.Reply("Group JID not found in contacts. Try syncing contacts with `sync` first.")
		return
	}
	handler.log.Debugln("Importing", jid, "for", user)
	portal := user.bridge.GetPortalByJID(database.GroupPortalKey(jid))
	if len(portal.MXID) > 0 {
		portal.Sync(user, contact)
		ce.Reply("Portal room synced.")
	} else {
		portal.Sync(user, contact)
		ce.Reply("Portal room created.")
	}
}

const cmdCreateHelp = `create <_subject_> <_international phone number_>,... - Create the group.`

func (handler *CommandHandler) CommandCreate(ce *CommandEvent) {
	if len(ce.Args) < 2 {
		ce.Reply("**Usage:** `create <subject> <international phone number>,...`")
		return
	}

	user := ce.User
	subject := ce.Args[0]
	userNumbers := strings.Split(ce.Args[1], ",")

	for i, number := range userNumbers {
		userNumbers[i] = number + whatsappExt.NewUserSuffix
	}

	handler.log.Debugln("Create Group", subject, "with", userNumbers)
	err := user.Conn.HandleGroupCreate(subject, userNumbers)
	if err != nil {
		ce.Reply("Please confirm that parameters is correct.")
	}
}
