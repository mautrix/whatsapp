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

package main

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

func (br *WABridge) CreatePrivatePortal(roomID id.RoomID, brInviter bridge.User, brGhost bridge.Ghost) {
	inviter := brInviter.(*User)
	puppet := brGhost.(*Puppet)
	key := database.NewPortalKey(puppet.JID, inviter.JID)
	portal := br.GetPortalByJID(key)
	log := br.ZLog.With().
		Str("action", "create private portal").
		Stringer("target_room_id", roomID).
		Stringer("inviter_mxid", inviter.MXID).
		Stringer("invitee_jid", puppet.JID).
		Logger()
	ctx := log.WithContext(context.TODO())

	if len(portal.MXID) == 0 {
		br.createPrivatePortalFromInvite(ctx, roomID, inviter, puppet, portal)
		return
	}

	ok := portal.ensureUserInvited(ctx, inviter)
	if !ok {
		log.Warn().Msg("Failed to invite user to existing private chat portal. Redirecting portal to new room...")
		br.createPrivatePortalFromInvite(ctx, roomID, inviter, puppet, portal)
		return
	}
	intent := puppet.DefaultIntent()
	errorMessage := fmt.Sprintf("You already have a private chat portal with me at [%s](%s)", portal.MXID, portal.MXID.URI(br.Config.Homeserver.Domain).MatrixToURL())
	errorContent := format.RenderMarkdown(errorMessage, true, false)
	_, _ = intent.SendMessageEvent(ctx, roomID, event.EventMessage, errorContent)
	log.Debug().Msg("Leaving private chat room from invite as we already have chat with the user")
	_, _ = intent.LeaveRoom(ctx, roomID)
}

func (br *WABridge) createPrivatePortalFromInvite(ctx context.Context, roomID id.RoomID, inviter *User, puppet *Puppet, portal *Portal) {
	log := zerolog.Ctx(ctx)
	// TODO check if room is already encrypted
	var existingEncryption event.EncryptionEventContent
	var encryptionEnabled bool
	err := portal.MainIntent().StateEvent(ctx, roomID, event.StateEncryption, "", &existingEncryption)
	if err != nil {
		log.Err(err).Msg("Failed to check if encryption is enabled")
	} else {
		encryptionEnabled = existingEncryption.Algorithm == id.AlgorithmMegolmV1
	}
	portal.MXID = roomID
	portal.updateLogger()
	portal.Topic = PrivateChatTopic
	portal.Name = puppet.Displayname
	portal.AvatarURL = puppet.AvatarURL
	portal.Avatar = puppet.Avatar
	log.Info().Msg("Created private chat portal from invite")
	intent := puppet.DefaultIntent()

	if br.Config.Bridge.Encryption.Default || encryptionEnabled {
		_, err = intent.InviteUser(ctx, roomID, &mautrix.ReqInviteUser{UserID: br.Bot.UserID})
		if err != nil {
			log.Err(err).Msg("Failed to invite bridge bot to enable e2be")
		}
		err = br.Bot.EnsureJoined(ctx, roomID)
		if err != nil {
			log.Err(err).Msg("Failed to join as bridge bot to enable e2be")
		}
		if !encryptionEnabled {
			_, err = intent.SendStateEvent(ctx, roomID, event.StateEncryption, "", portal.GetEncryptionEventContent())
			if err != nil {
				log.Err(err).Msg("Failed to enable e2be")
			}
		}
		br.AS.StateStore.SetMembership(ctx, roomID, inviter.MXID, event.MembershipJoin)
		br.AS.StateStore.SetMembership(ctx, roomID, puppet.MXID, event.MembershipJoin)
		br.AS.StateStore.SetMembership(ctx, roomID, br.Bot.UserID, event.MembershipJoin)
		portal.Encrypted = true
	}
	_, _ = portal.MainIntent().SetRoomTopic(ctx, portal.MXID, portal.Topic)
	if portal.shouldSetDMRoomMetadata() {
		_, err = portal.MainIntent().SetRoomName(ctx, portal.MXID, portal.Name)
		portal.NameSet = err == nil
		_, err = portal.MainIntent().SetRoomAvatar(ctx, portal.MXID, portal.AvatarURL)
		portal.AvatarSet = err == nil
	}
	err = portal.Update(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to save portal to database after creating from invite")
	}
	portal.UpdateBridgeInfo(ctx)
	_, _ = intent.SendNotice(ctx, roomID, "Private chat portal created")
}

func (br *WABridge) HandlePresence(ctx context.Context, evt *event.Event) {
	user := br.GetUserByMXIDIfExists(evt.Sender)
	if user == nil || !user.IsLoggedIn() {
		return
	}
	customPuppet := br.GetPuppetByCustomMXID(user.MXID)
	// TODO move this flag to the user and/or portal data
	if customPuppet != nil && !customPuppet.EnablePresence {
		return
	}

	presence := types.PresenceAvailable
	if evt.Content.AsPresence().Presence != event.PresenceOnline {
		presence = types.PresenceUnavailable
		user.zlog.Debug().Msg("Marking offline")
	} else {
		user.zlog.Debug().Msg("Marking online")
	}
	user.lastPresence = presence
	if user.Client.Store.PushName != "" {
		err := user.Client.SendPresence(presence)
		if err != nil {
			user.zlog.Err(err).Msg("Failed to set presence")
		}
	}
}
