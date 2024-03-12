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
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
)

var userIDRegex *regexp.Regexp

func (br *WABridge) ParsePuppetMXID(mxid id.UserID) (jid types.JID, ok bool) {
	if userIDRegex == nil {
		userIDRegex = br.Config.MakeUserIDRegex("([0-9]+)")
	}
	match := userIDRegex.FindStringSubmatch(string(mxid))
	if len(match) == 2 {
		jid = types.NewJID(match[1], types.DefaultUserServer)
		ok = true
	}
	return
}

func (br *WABridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	jid, ok := br.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return br.GetPuppetByJID(jid)
}

func (br *WABridge) GetPuppetByJID(jid types.JID) *Puppet {
	ctx := context.TODO()
	jid = jid.ToNonAD()
	if jid.Server == types.LegacyUserServer {
		jid.Server = types.DefaultUserServer
	} else if jid.Server != types.DefaultUserServer {
		return nil
	}
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()
	puppet, ok := br.puppets[jid]
	if !ok {
		dbPuppet, err := br.DB.Puppet.Get(ctx, jid)
		if err != nil {
			br.ZLog.Err(err).Stringer("jid", jid).Msg("Failed to get puppet from database")
			return nil
		}
		if dbPuppet == nil {
			dbPuppet = br.DB.Puppet.New()
			dbPuppet.JID = jid
			err = dbPuppet.Insert(ctx)
			if err != nil {
				br.ZLog.Err(err).Stringer("jid", jid).Msg("Failed to insert new puppet to database")
				return nil
			}
		}
		puppet = br.NewPuppet(dbPuppet)
		br.puppets[puppet.JID] = puppet
		if len(puppet.CustomMXID) > 0 {
			br.puppetsByCustomMXID[puppet.CustomMXID] = puppet
		}
	}
	return puppet
}

func (br *WABridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()
	puppet, ok := br.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet, err := br.DB.Puppet.GetByCustomMXID(context.TODO(), mxid)
		if err != nil {
			br.ZLog.Err(err).Stringer("mxid", mxid).Msg("Failed to get puppet by custom mxid from database")
		}
		if dbPuppet == nil {
			return nil
		}
		puppet = br.NewPuppet(dbPuppet)
		br.puppets[puppet.JID] = puppet
		br.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	return puppet
}

func (user *User) GetIDoublePuppet() bridge.DoublePuppet {
	p := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if p == nil || p.CustomIntent() == nil {
		return nil
	}
	return p
}

func (user *User) GetIGhost() bridge.Ghost {
	if user.JID.IsEmpty() {
		return nil
	}
	p := user.bridge.GetPuppetByJID(user.JID)
	if p == nil {
		return nil
	}
	return p
}

func (br *WABridge) IsGhost(id id.UserID) bool {
	_, ok := br.ParsePuppetMXID(id)
	return ok
}

func (br *WABridge) GetIGhost(id id.UserID) bridge.Ghost {
	p := br.GetPuppetByMXID(id)
	if p == nil {
		return nil
	}
	return p
}

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

func (br *WABridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAllWithCustomMXID(context.TODO()))
}

func (br *WABridge) GetAllPuppets() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAll(context.TODO()))
}

func (br *WABridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet, err error) []*Puppet {
	if err != nil {
		br.ZLog.Err(err).Msg("Error getting puppets from database")
		return nil
	}
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()
	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}
		puppet, ok := br.puppets[dbPuppet.JID]
		if !ok {
			puppet = br.NewPuppet(dbPuppet)
			br.puppets[dbPuppet.JID] = puppet
			if len(dbPuppet.CustomMXID) > 0 {
				br.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}
		output[index] = puppet
	}
	return output
}

func (br *WABridge) FormatPuppetMXID(jid types.JID) id.UserID {
	return id.NewUserID(
		br.Config.Bridge.FormatUsername(jid.User),
		br.Config.Homeserver.Domain)
}

func (br *WABridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: br,
		zlog:   br.ZLog.With().Stringer("puppet_jid", dbPuppet.JID).Logger(),

		MXID: br.FormatPuppetMXID(dbPuppet.JID),
	}
}

type Puppet struct {
	*database.Puppet

	bridge *WABridge
	zlog   zerolog.Logger

	typingIn id.RoomID
	typingAt time.Time

	MXID id.UserID

	customIntent *appservice.IntentAPI
	customUser   *User

	syncLock sync.Mutex
}

var _ bridge.GhostWithProfile = (*Puppet)(nil)

func (puppet *Puppet) GetDisplayname() string {
	return puppet.Displayname
}

func (puppet *Puppet) GetAvatarURL() id.ContentURI {
	return puppet.AvatarURL
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if puppet.customIntent == nil || portal.Key.JID == puppet.JID || (portal.Key.JID.Server == types.BroadcastServer && portal.Key.Receiver != puppet.JID) {
		return puppet.DefaultIntent()
	}
	return puppet.customIntent
}

func (puppet *Puppet) CustomIntent() *appservice.IntentAPI {
	return puppet.customIntent
}

func (puppet *Puppet) DefaultIntent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

func (puppet *Puppet) UpdateAvatar(ctx context.Context, source *User, forcePortalSync bool) bool {
	changed := source.updateAvatar(ctx, puppet.JID, false, &puppet.Avatar, &puppet.AvatarURL, &puppet.AvatarSet, puppet.DefaultIntent())
	if !changed || puppet.Avatar == "unauthorized" {
		if forcePortalSync {
			go puppet.updatePortalAvatar(ctx)
		}
		return changed
	}
	err := puppet.DefaultIntent().SetAvatarURL(ctx, puppet.AvatarURL)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to set avatar from puppet")
	} else {
		puppet.AvatarSet = true
	}
	go puppet.updatePortalAvatar(ctx)
	return true
}

func (puppet *Puppet) UpdateName(ctx context.Context, contact types.ContactInfo, forcePortalSync bool) bool {
	newName, quality := puppet.bridge.Config.Bridge.FormatDisplayname(puppet.JID, contact)
	if (puppet.Displayname != newName || !puppet.NameSet) && quality >= puppet.NameQuality {
		oldName := puppet.Displayname
		puppet.Displayname = newName
		puppet.NameQuality = quality
		puppet.NameSet = false
		err := puppet.DefaultIntent().SetDisplayName(ctx, newName)
		if err == nil {
			puppet.zlog.Debug().Str("old_name", oldName).Str("new_name", newName).Msg("Updated name")
			puppet.NameSet = true
			go puppet.updatePortalName(ctx)
		} else {
			puppet.zlog.Err(err).Msg("Failed to set displayname")
		}
		return true
	} else if forcePortalSync {
		go puppet.updatePortalName(ctx)
	}
	return false
}

func (puppet *Puppet) UpdateContactInfo(ctx context.Context) bool {
	if !puppet.bridge.SpecVersions.Supports(mautrix.BeeperFeatureArbitraryProfileMeta) {
		return false
	}

	if puppet.ContactInfoSet {
		return false
	}

	contactInfo := map[string]any{
		"com.beeper.bridge.identifiers": []string{
			fmt.Sprintf("tel:+%s", puppet.JID.User),
			fmt.Sprintf("whatsapp:%s", puppet.JID.String()),
		},
		"com.beeper.bridge.remote_id": puppet.JID.String(),
		"com.beeper.bridge.service":   "whatsapp",
		"com.beeper.bridge.network":   "whatsapp",
	}
	err := puppet.DefaultIntent().BeeperUpdateProfile(ctx, contactInfo)
	if err != nil {
		puppet.zlog.Err(err).Msg("Failed to store custom contact info in profile")
		return false
	} else {
		puppet.ContactInfoSet = true
		return true
	}
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	for _, portal := range puppet.bridge.GetAllPortalsByJID(puppet.JID) {
		// Get room create lock to prevent races between receiving contact info and room creation.
		portal.roomCreateLock.Lock()
		meta(portal)
		portal.roomCreateLock.Unlock()
	}
}

func (puppet *Puppet) updatePortalAvatar(ctx context.Context) {
	puppet.updatePortalMeta(func(portal *Portal) {
		if portal.Avatar == puppet.Avatar && portal.AvatarURL == puppet.AvatarURL && (portal.AvatarSet || !portal.shouldSetDMRoomMetadata()) {
			return
		}
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.AvatarSet = false
		if len(portal.MXID) > 0 && !portal.shouldSetDMRoomMetadata() {
			portal.UpdateBridgeInfo(ctx)
		} else if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomAvatar(ctx, portal.MXID, puppet.AvatarURL)
			if err != nil {
				portal.zlog.Err(err).Msg("Failed to set avatar from puppet")
			} else {
				portal.AvatarSet = true
				portal.UpdateBridgeInfo(ctx)
			}
		}
		err := portal.Update(ctx)
		if err != nil {
			portal.zlog.Err(err).Msg("Failed to save portal after updating avatar from puppet")
		}
	})
}

func (puppet *Puppet) updatePortalName(ctx context.Context) {
	puppet.updatePortalMeta(func(portal *Portal) {
		portal.UpdateName(ctx, puppet.Displayname, types.EmptyJID, true)
	})
}

func (puppet *Puppet) SyncContact(ctx context.Context, source *User, onlyIfNoName, shouldHavePushName bool, reason string) {
	if puppet == nil {
		return
	}
	if onlyIfNoName && len(puppet.Displayname) > 0 && (!shouldHavePushName || puppet.NameQuality > config.NameQualityPhone) {
		source.EnqueuePuppetResync(puppet)
		return
	}
	log := zerolog.Ctx(ctx).With().
		Str("method", "Puppet.SyncContact").
		Stringer("puppet_jid", puppet.JID).
		Stringer("source_user_jid", source.JID).
		Stringer("source_user_mxid", source.MXID).
		Logger()
	ctx = log.WithContext(ctx)

	contact, err := source.Client.Store.Contacts.GetContact(puppet.JID)
	if err != nil {
		log.Err(err).
			Stringer("source_mxid", source.MXID).
			Str("sync_reason", reason).
			Msg("Failed to get contact info through user in SyncContact")
	} else if !contact.Found {
		log.Warn().
			Stringer("source_mxid", source.MXID).
			Str("sync_reason", reason).
			Msg("No contact info found through user in SyncContact")
	}
	puppet.syncInternal(ctx, source, &contact, false, false)
}

func (puppet *Puppet) Sync(ctx context.Context, source *User, contact *types.ContactInfo, forceAvatarSync, forcePortalSync bool) {
	log := zerolog.Ctx(ctx).With().
		Str("method", "Puppet.Sync").
		Stringer("puppet_jid", puppet.JID).
		Stringer("source_user_jid", source.JID).
		Stringer("source_user_mxid", source.MXID).
		Logger()
	ctx = log.WithContext(ctx)
	puppet.syncInternal(ctx, source, contact, forceAvatarSync, forcePortalSync)
}

func (puppet *Puppet) syncInternal(ctx context.Context, source *User, contact *types.ContactInfo, forceAvatarSync, forcePortalSync bool) {
	log := zerolog.Ctx(ctx)
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()
	err := puppet.DefaultIntent().EnsureRegistered(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to ensure registered")
	}

	log.Debug().Stringer("source_jid", source.JID).Msg("Syncing info through user")

	update := false
	if contact != nil {
		if puppet.JID.User == source.JID.User {
			contact.PushName = source.Client.Store.PushName
		}
		update = puppet.UpdateName(ctx, *contact, forcePortalSync) || update
	}
	if len(puppet.Avatar) == 0 || forceAvatarSync || puppet.bridge.Config.Bridge.UserAvatarSync {
		update = puppet.UpdateAvatar(ctx, source, forcePortalSync) || update
	}
	update = puppet.UpdateContactInfo(ctx) || update
	if update || puppet.LastSync.Add(24*time.Hour).Before(time.Now()) {
		puppet.LastSync = time.Now()
		err = puppet.Update(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save puppet after sync")
		}
	}
}
