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
	"fmt"
	"regexp"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	log "maunium.net/go/maulogger/v2"

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
		dbPuppet := br.DB.Puppet.Get(jid)
		if dbPuppet == nil {
			dbPuppet = br.DB.Puppet.New()
			dbPuppet.JID = jid
			dbPuppet.Insert()
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
		dbPuppet := br.DB.Puppet.GetByCustomMXID(mxid)
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
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAllWithCustomMXID())
}

func (br *WABridge) GetAllPuppets() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAll())
}

func (br *WABridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
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
		log:    br.Log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.JID)),

		MXID: br.FormatPuppetMXID(dbPuppet.JID),
	}
}

type Puppet struct {
	*database.Puppet

	bridge *WABridge
	log    log.Logger

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

func (puppet *Puppet) UpdateAvatar(source *User, forcePortalSync bool) bool {
	changed := source.updateAvatar(puppet.JID, false, &puppet.Avatar, &puppet.AvatarURL, &puppet.AvatarSet, puppet.log, puppet.DefaultIntent())
	if !changed || puppet.Avatar == "unauthorized" {
		if forcePortalSync {
			go puppet.updatePortalAvatar()
		}
		return changed
	}
	err := puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warnln("Failed to set avatar:", err)
	} else {
		puppet.AvatarSet = true
	}
	go puppet.updatePortalAvatar()
	return true
}

func (puppet *Puppet) UpdateName(contact types.ContactInfo, forcePortalSync bool) bool {
	newName, quality := puppet.bridge.Config.Bridge.FormatDisplayname(puppet.JID, contact)
	if (puppet.Displayname != newName || !puppet.NameSet) && quality >= puppet.NameQuality {
		oldName := puppet.Displayname
		puppet.Displayname = newName
		puppet.NameQuality = quality
		puppet.NameSet = false
		err := puppet.DefaultIntent().SetDisplayName(newName)
		if err == nil {
			puppet.log.Debugln("Updated name", oldName, "->", newName)
			puppet.NameSet = true
			go puppet.updatePortalName()
		} else {
			puppet.log.Warnln("Failed to set display name:", err)
		}
		return true
	} else if forcePortalSync {
		go puppet.updatePortalName()
	}
	return false
}

func (puppet *Puppet) UpdateContactInfo() bool {
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
	err := puppet.DefaultIntent().BeeperUpdateProfile(contactInfo)
	if err != nil {
		puppet.log.Warnln("Failed to store custom contact info in profile:", err)
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

func (puppet *Puppet) updatePortalAvatar() {
	puppet.updatePortalMeta(func(portal *Portal) {
		if portal.Avatar == puppet.Avatar && portal.AvatarURL == puppet.AvatarURL && (portal.AvatarSet || !portal.shouldSetDMRoomMetadata()) {
			return
		}
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.AvatarSet = false
		defer portal.Update(nil)
		if len(portal.MXID) > 0 && !portal.shouldSetDMRoomMetadata() {
			portal.UpdateBridgeInfo()
		} else if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, puppet.AvatarURL)
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			} else {
				portal.AvatarSet = true
				portal.UpdateBridgeInfo()
			}
		}
	})
}

func (puppet *Puppet) updatePortalName() {
	puppet.updatePortalMeta(func(portal *Portal) {
		portal.UpdateName(puppet.Displayname, types.EmptyJID, true)
	})
}

func (puppet *Puppet) SyncContact(source *User, onlyIfNoName, shouldHavePushName bool, reason string) {
	if onlyIfNoName && len(puppet.Displayname) > 0 && (!shouldHavePushName || puppet.NameQuality > config.NameQualityPhone) {
		source.EnqueuePuppetResync(puppet)
		return
	}

	contact, err := source.Client.Store.Contacts.GetContact(puppet.JID)
	if err != nil {
		puppet.log.Warnfln("Failed to get contact info through %s in SyncContact: %v (sync reason: %s)", source.MXID, reason)
	} else if !contact.Found {
		puppet.log.Warnfln("No contact info found through %s in SyncContact (sync reason: %s)", source.MXID, reason)
	}
	puppet.Sync(source, &contact, false, false)
}

func (puppet *Puppet) Sync(source *User, contact *types.ContactInfo, forceAvatarSync, forcePortalSync bool) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()
	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	puppet.log.Debugfln("Syncing info through %s", source.JID)

	update := false
	if contact != nil {
		if puppet.JID.User == source.JID.User {
			contact.PushName = source.Client.Store.PushName
		}
		update = puppet.UpdateName(*contact, forcePortalSync) || update
	}
	if len(puppet.Avatar) == 0 || forceAvatarSync || puppet.bridge.Config.Bridge.UserAvatarSync {
		update = puppet.UpdateAvatar(source, forcePortalSync) || update
	}
	update = puppet.UpdateContactInfo() || update
	if update || puppet.LastSync.Add(24*time.Hour).Before(time.Now()) {
		puppet.LastSync = time.Now()
		puppet.Update()
	}
}
