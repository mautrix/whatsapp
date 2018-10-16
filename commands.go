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
	"strings"

	"maunium.net/go/maulogger"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix-whatsapp/types"
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
	_, err := ce.Bot.SendNotice(string(ce.RoomID), msg)
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
	cmdPrefix := handler.bridge.Config.Bridge.CommandPrefix + " "
	ce.Reply(strings.Join([]string{
		cmdPrefix + cmdHelpHelp,
		cmdPrefix + cmdLoginHelp,
		cmdPrefix + cmdLogoutHelp,
	}, "\n"))
}
