// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package connector

import (
	"maunium.net/go/mautrix/bridgev2/commands"

	"time"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
	"go.mau.fi/util/jsontime"
)

var (
	HelpSectionInvites = commands.HelpSection{Name: "Group invites", Order: 25}
	HelpSectionGroups  = commands.HelpSection{Name: "Groups", Order: 30}
)

var cmdAccept = &commands.FullHandler{
	Func: fnAccept,
	Name: "accept",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Accept a group invite. This can only be used in reply to a group invite message.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdListGroups = &commands.FullHandler{
	Func: fnListGroups,
	Name: "list-groups",
	Help: commands.HelpMeta{
		Section:     HelpSectionGroups,
		Description: "List all WhatsApp groups you are a member of.",
	},
	RequiresLogin: true,
}

func fnAccept(ce *commands.Event) {
	if len(ce.ReplyTo) == 0 {
		ce.Reply("You must reply to a group invite message when using this command.")
	} else if message, err := ce.Bridge.DB.Message.GetPartByMXID(ce.Ctx, ce.ReplyTo); err != nil {
		ce.Log.Err(err).Stringer("reply_to_mxid", ce.ReplyTo).Msg("Failed to get reply target event to handle !wa accept command")
		ce.Reply("Failed to get reply event")
	} else if message == nil {
		ce.Log.Warn().Stringer("reply_to_mxid", ce.ReplyTo).Msg("Reply target event not found to handle !wa accept command")
		ce.Reply("Reply event not found")
	} else if meta := message.Metadata.(*waid.MessageMetadata).GroupInvite; meta == nil {
		ce.Reply("That doesn't look like a group invite message.")
	} else if meta.Inviter.User == waid.ParseUserLoginID(ce.Portal.Receiver, 0).User {
		ce.Reply("You can't accept your own invites")
	} else if login := ce.Bridge.GetCachedUserLoginByID(ce.Portal.Receiver); login == nil {
		ce.Reply("Login not found")
	} else if !login.Client.IsLoggedIn() {
		ce.Reply("Not logged in")
	} else if err = login.Client.(*WhatsAppClient).Client.JoinGroupWithInvite(meta.JID, meta.Inviter, meta.Code, meta.Expiration); err != nil {
		ce.Log.Err(err).Msg("Failed to accept group invite")
		ce.Reply("Failed to accept group invite: %v", err)
	} else {
		ce.Reply("Successfully accepted the invite, the portal should be created momentarily")
	}
}

func fnListGroups(ce *commands.Event) {
	if login := ce.User.GetDefaultLogin(); login == nil {
		ce.Reply("No WhatsApp account found. Please use !wa login to connect your WhatsApp account.")
	} else if !login.Client.IsLoggedIn() {
		ce.Reply("Not logged in")
	} else {
		// Set LastHistorySync to 24 hours ago to force a new sync
		loginMetadata := login.Metadata.(*waid.UserLoginMetadata)
		loginMetadata.LastHistorySync = jsontime.Unix{Time: time.Now().Add(-24 * time.Hour)}
		ce.Log.Debug().Time("history_sync_reset_to", loginMetadata.LastHistorySync.Time).Msg("Reset LastHistorySync to 24 hours ago")

		// Save the updated metadata
		err := login.Save(ce.Ctx)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to save updated LastHistorySync timestamp")
		}

		// Proceed with sending groups to ReMatch backend
		if err := login.Client.(*WhatsAppClient).SendGroupsToReMatchBackend(ce.Ctx); err != nil {
			ce.Log.Err(err).Msg("Failed to send groups to ReMatch backend")
			ce.Reply("Failed to send groups to ReMatch backend: %v", err)
		} else {
			ce.Reply("Successfully sent your WhatsApp groups to ReMatch backend.")
		}
	}
}
