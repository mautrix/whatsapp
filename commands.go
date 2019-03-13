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
func (ce *CommandEvent) Reply(msg string) {
	content := format.RenderMarkdown(msg)
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
	switch cmd {
	case "login":
		handler.CommandLogin(ce)
	case "logout":
		handler.CommandLogout(ce)
	case "help":
		handler.CommandHelp(ce)
	case "sync":
		handler.CommandSync(ce)
	case "list":
		handler.CommandList(ce)
	case "open":
		handler.CommandOpen(ce)
	case "pm":
		handler.CommandPM(ce)
	default:
		ce.Reply("Unknown Command")
	}
}

const cmdLoginHelp = `login - Authenticate this Bridge as WhatsApp Web Client`

// CommandLogin handles login command
func (handler *CommandHandler) CommandLogin(ce *CommandEvent) {
	if ce.User.Session != nil {
		ce.Reply("You're already logged in.")
		return
	}

	ce.User.Connect(true)
	ce.User.Login(ce.RoomID)
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
		ce.Reply("Error while logging out (see logs for details)")
		return
	}
	ce.User.Conn = nil
	ce.User.Session = nil
	ce.User.Update()
	ce.Reply("Logged out successfully.")
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
	ce.Reply(fmt.Sprintf("### Contacts\n%s\n\n### Groups\n%s", contacts.String(), groups.String()))
}

const cmdOpenHelp = `open <_group JID_> - Open a group chat portal.`

func (handler *CommandHandler) CommandOpen(ce *CommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `open <group JID>`")
	}

	user := ce.User
	jid := ce.Args[0]

	if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
		ce.Reply(fmt.Sprintf("That looks like a user JID. Did you mean `pm %s`?", jid[:len(jid)-len(whatsappExt.NewUserSuffix)]))
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
		ce.Reply(fmt.Sprintf("Failed to create portal room: %v", err))
		return
	}
	ce.Reply("Created portal room and invited you to it.")
}
