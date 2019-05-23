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
	"github.com/Rhymen/go-whatsapp"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/format"
	"strings"

	"maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix-appservice"

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
	Args    []string
}

// Reply sends a reply to command as notice
func (ce *CommandEvent) Reply(msg string, args ...interface{}) {
	content := format.RenderMarkdown(fmt.Sprintf(msg, args...))
	content.MsgType = mautrix.MsgNotice
	_, err := ce.Bot.SendMessageEvent(ce.User.ManagementRoom, mautrix.EventMessage, content)
	if err != nil {
		ce.Handler.log.Warnfln("Failed to reply to command from %s: %v", ce.User.MXID, err)
	}
}

// Handle handles messages to the bridge
func (handler *CommandHandler) Handle(roomID types.MatrixRoomID, user *User, message string) {
	args := strings.Split(message, " ")
	cmd := strings.ToLower(args[0])
	ce := &CommandEvent{
		Bot:     handler.bridge.Bot,
		Bridge:  handler.bridge,
		Handler: handler,
		RoomID:  roomID,
		User:    user,
		Args:    args[1:],
	}
	handler.log.Debugfln("%s sent '%s' in %s", user.MXID, message, roomID)
	switch cmd {
	case "login":
		handler.CommandLogin(ce)
	case "help":
		handler.CommandHelp(ce)
	case "reconnect":
		handler.CommandReconnect(ce)
	case "disconnect":
		handler.CommandDisconnect(ce)
	case "delete-connection":
		handler.CommandDeleteConnection(ce)
	case "delete-session":
		handler.CommandDeleteSession(ce)
	case "delete-portal":
		handler.CommandDeletePortal(ce)
	case "logout", "sync", "list", "open", "pm":
		if ce.User.Conn == nil {
			ce.Reply("You are not logged in. Use the `login` command to log into WhatsApp.")
			return
		} else if !ce.User.Connected {
			ce.Reply("You are not connected to WhatsApp. Use the `reconnect` command to reconnect.")
			return
		}

		switch cmd {
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
		}
	default:
		ce.Reply("Unknown Command")
	}
}

const cmdLoginHelp = `login - Authenticate this Bridge as WhatsApp Web Client`

// CommandLogin handles login command
func (handler *CommandHandler) CommandLogin(ce *CommandEvent) {
	if ce.User.Conn == nil {
		if !ce.User.Connect(true) {
			ce.User.log.Debugln("Connect() returned false, assuming error was logged elsewhere and canceling login.")
			return
		}
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
	ce.User.Connected = false
	ce.User.Conn.RemoveHandlers()
	ce.User.Conn = nil
	ce.User.SetSession(nil)
	ce.Reply("Logged out successfully.")
}

const cmdDeleteSessionHelp = `delete-session - Delete session information and disconnect from WhatsApp without sending a logout request`

func (handler *CommandHandler) CommandDeleteSession(ce *CommandEvent) {
	if ce.User.Session == nil && !ce.User.Connected && ce.User.Conn == nil {
		ce.Reply("Nothing to purge: no session information stored and no active connection.")
		return
	}
	ce.User.SetSession(nil)
	ce.User.Connected = false
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
	err := ce.User.Conn.Restore()
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
	}
	if err != nil {
		ce.User.log.Warnln("Error while reconnecting:", err)
		if err == whatsapp.ErrAlreadyLoggedIn {
			if ce.User.Connected {
				ce.Reply("You were already connected.")
			} else {
				ce.User.Connected = true
				ce.User.ConnectionErrors = 0
				ce.Reply("You were already connected, but the bridge hadn't noticed. Fixed that now.")
			}
		} else if err.Error() == "restore session connection timed out" {
			ce.Reply("Reconnection timed out. Is WhatsApp on your phone reachable?")
		} else {
			ce.Reply("Unknown error while reconnecting: %v", err)
		}
		return
	}
	ce.User.Connected = true
	ce.User.ConnectionErrors = 0
	ce.Reply("Reconnected successfully.")
	go ce.User.PostLogin()
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
	ce.User.Connected = false
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
	ce.User.Connected = false
	if err == whatsapp.ErrNotConnected {
		ce.Reply("You were not connected.")
		return
	} else if err != nil {
		ce.User.log.Warnln("Error while disconnecting:", err)
		ce.Reply("Unknown error while disconnecting: %v", err)
		return
	}
	ce.User.SetSession(&sess)
	ce.Reply("Successfully disconnected. Use the `reconnect` command to reconnect.")
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
		cmdPrefix + cmdLoginHelp,
		cmdPrefix + cmdLogoutHelp,
		cmdPrefix + cmdDeleteSessionHelp,
		cmdPrefix + cmdReconnectHelp,
		cmdPrefix + cmdDisconnectHelp,
		cmdPrefix + cmdSyncHelp,
		cmdPrefix + cmdListHelp,
		cmdPrefix + cmdOpenHelp,
		cmdPrefix + cmdPMHelp,
	}, "\n* "))
}

const cmdSyncHelp = `sync [--create] - Synchronize contacts from phone and optionally create portals for group chats.`

// CommandSync handles sync command
func (handler *CommandHandler) CommandSync(ce *CommandEvent) {
	user := ce.User
	create := len(ce.Args) > 0 && ce.Args[0] == "--create"

	handler.log.Debugln("Importing all contacts of", user)
	_, err := user.Conn.Contacts()
	if err != nil {
		handler.log.Errorln("Error on update of contacts of user", user, ":", err)
		return
	}

	for jid, contact := range user.Conn.Store.Contacts {
		if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
			puppet := user.bridge.GetPuppetByJID(contact.Jid)
			puppet.Sync(user, contact)
		} else {
			portal := user.bridge.GetPortalByJID(database.GroupPortalKey(contact.Jid))
			if len(portal.MXID) > 0 || create {
				portal.Sync(user, contact)
			}
		}
	}

	ce.Reply("Imported contacts successfully.")
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
