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
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var (
	HelpSectionInvites = commands.HelpSection{Name: "Group invites", Order: 25}
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

var cmdSync = &commands.FullHandler{
	Func: fnSync,
	Name: "sync",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Sync data from WhatsApp.",
		Args:        "<group/groups/contacts>",
	},
	RequiresLogin: true,
}

func fnSync(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Login not found")
		return
	}
	if len(ce.Args) == 0 {
		ce.Reply("Usage: `$cmdprefix sync <group/groups/contacts/contacts-with-avatars/appstate>`")
		return
	}
	logContext := func(c zerolog.Context) zerolog.Context {
		return c.Stringer("triggered_by_user", ce.User.MXID)
	}
	wa := login.Client.(*WhatsAppClient)
	switch strings.ToLower(ce.Args[0]) {
	case "group", "portal", "room":
		if ce.Portal == nil {
			ce.Reply("`!wa sync group` can only be used in a portal room.")
			return
		}
		login.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:       bridgev2.RemoteEventChatResync,
				PortalKey:  ce.Portal.PortalKey,
				LogContext: logContext,
			},
			GetChatInfoFunc: wa.GetChatInfo,
		})
		ce.React("✅")
	case "groups":
		groups, err := wa.Client.GetJoinedGroups()
		if err != nil {
			ce.Reply("Failed to get joined groups: %v", err)
			return
		}
		for _, group := range groups {
			wrapped := wa.wrapGroupInfo(group)
			wrapped.ExtraUpdates = bridgev2.MergeExtraUpdaters(wrapped.ExtraUpdates, updatePortalLastSyncAt)
			wa.addExtrasToWrapped(ce.Ctx, group.JID, wrapped, nil)
			login.QueueRemoteEvent(&simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    wa.makeWAPortalKey(group.JID),
					LogContext:   logContext,
					CreatePortal: true,
				},
				ChatInfo: wrapped,
			})
		}
		ce.Reply("Queued syncs for %d groups", len(groups))
	case "contacts":
		wa.resyncContacts(false)
		ce.React("✅")
	case "contacts-with-avatars":
		wa.resyncContacts(true)
		ce.React("✅")
	case "appstate":
		for _, name := range appstate.AllPatchNames {
			err := wa.Client.FetchAppState(ce.Ctx, name, true, false)
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
	default:
		ce.Reply("Unknown sync target `%s`", ce.Args[0])
	}
}

var cmdInviteLink = &commands.FullHandler{
	Func: fnInviteLink,
	Name: "invite-link",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Get an invite link to the current group chat, optionally regenerating the link and revoking the old link.",
		Args:        "[--reset]",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

func fnInviteLink(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Login not found")
		return
	}
	portalJID, err := waid.ParsePortalID(ce.Portal.ID)
	if err != nil {
		ce.Reply("Failed to parse portal ID: %v", err)
		return
	}

	wa := login.Client.(*WhatsAppClient)
	reset := len(ce.Args) > 0 && strings.ToLower(ce.Args[0]) == "--reset"
	if portalJID.Server == types.DefaultUserServer || portalJID.Server == types.HiddenUserServer {
		ce.Reply("Can't get invite link to private chat")
	} else if portalJID.IsBroadcastList() {
		ce.Reply("Can't get invite link to broadcast list")
	} else if link, err := wa.Client.GetGroupInviteLink(portalJID, reset); err != nil {
		ce.Reply("Failed to get invite link: %v", err)
	} else {
		ce.Reply(link)
	}
}

var cmdResolveLink = &commands.FullHandler{
	Func: fnResolveLink,
	Name: "resolve-link",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Resolve a WhatsApp group invite or business message link.",
		Args:        "<_group, contact, or message link_>",
	},
	RequiresLogin: true,
}

func fnResolveLink(ce *commands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `$cmdprefix resolve-link <group or message link>`")
		return
	}
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Login not found")
		return
	}
	wa := login.Client.(*WhatsAppClient)
	if strings.HasPrefix(ce.Args[0], whatsmeow.InviteLinkPrefix) {
		group, err := wa.Client.GetGroupInfoFromLink(ce.Args[0])
		if err != nil {
			ce.Reply("Failed to get group info: %v", err)
			return
		}
		ce.Reply("That invite link points at %s (`%s`)", group.Name, group.JID)
	} else if strings.HasPrefix(ce.Args[0], whatsmeow.BusinessMessageLinkPrefix) || strings.HasPrefix(ce.Args[0], whatsmeow.BusinessMessageLinkDirectPrefix) {
		target, err := wa.Client.ResolveBusinessMessageLink(ce.Args[0])
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
	} else if strings.HasPrefix(ce.Args[0], whatsmeow.ContactQRLinkPrefix) || strings.HasPrefix(ce.Args[0], whatsmeow.ContactQRLinkDirectPrefix) {
		target, err := wa.Client.ResolveContactQRLink(ce.Args[0])
		if err != nil {
			ce.Reply("Failed to get contact info: %v", err)
			return
		}
		if target.PushName != "" {
			ce.Reply("That link points at %s (+%s)", target.PushName, target.JID.User)
		} else {
			ce.Reply("That link points at +%s", target.JID.User)
		}
	} else {
		ce.Reply("That doesn't look like a group invite link nor a business message link.")
	}
}

var cmdJoin = &commands.FullHandler{
	Func: fnJoin,
	Name: "join",
	Help: commands.HelpMeta{
		Section:     HelpSectionInvites,
		Description: "Join a group chat with an invite link.",
		Args:        "<_invite link_>",
	},
	RequiresLogin: true,
}

func fnJoin(ce *commands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage:** `$cmdprefix join <invite link>`")
		return
	}
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Login not found")
		return
	}
	wa := login.Client.(*WhatsAppClient)

	if strings.HasPrefix(ce.Args[0], whatsmeow.InviteLinkPrefix) {
		jid, err := wa.Client.JoinGroupWithLink(ce.Args[0])
		if err != nil {
			ce.Reply("Failed to join group: %v", err)
			return
		}
		ce.Log.Debug().Stringer("group_jid", jid).Msg("User successfully joined WhatsApp group with link")
		ce.Reply("Successfully joined group `%s`, the portal should be created momentarily", jid)
	} else if strings.HasPrefix(ce.Args[0], whatsmeow.NewsletterLinkPrefix) {
		info, err := wa.Client.GetNewsletterInfoWithInvite(ce.Args[0])
		if err != nil {
			ce.Reply("Failed to get channel info: %v", err)
			return
		}
		err = wa.Client.FollowNewsletter(info.ID)
		if err != nil {
			ce.Reply("Failed to follow channel: %v", err)
			return
		}
		ce.Log.Debug().Stringer("channel_jid", info.ID).Msg("User successfully followed WhatsApp channel with link")
		ce.Reply("Successfully followed channel `%s`, the portal should be created momentarily", info.ID)
	} else {
		ce.Reply("That doesn't look like a WhatsApp invite link")
	}
}
