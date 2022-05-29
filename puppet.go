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
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
)

var userIDRegex *regexp.Regexp

func (br *WABridge) ParsePuppetMXID(mxid id.UserID) (jid types.JID, ok bool) {
	if userIDRegex == nil {
		userIDRegex = regexp.MustCompile(fmt.Sprintf("^@%s:%s$",
			br.Config.Bridge.FormatUsername("([0-9]+)"),
			br.Config.Homeserver.Domain))
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

func reuploadAvatar(intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
	getResp, err := http.DefaultClient.Get(url)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}
	data, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to read avatar bytes: %w", err)
	}

	mime := http.DetectContentType(data)
	resp, err := intent.UploadBytes(data, mime)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}
	return resp.ContentURI, nil
}

func (puppet *Puppet) UpdateAvatar(source *User) bool {
	avatar, err := source.Client.GetProfilePictureInfo(puppet.JID, false)
	if err != nil {
		if !errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
			puppet.log.Warnln("Failed to get avatar URL:", err)
		} else if puppet.Avatar == "" {
			puppet.Avatar = "unauthorized"
			return true
		}
		return false
	} else if avatar == nil {
		if puppet.Avatar == "remove" {
			return false
		}
		puppet.AvatarURL = id.ContentURI{}
		avatar = &types.ProfilePictureInfo{ID: "remove"}
	} else if avatar.ID == puppet.Avatar {
		return false
	} else if len(avatar.URL) == 0 {
		puppet.log.Warnln("Didn't get URL in response to avatar query")
		return false
	} else {
		url, err := reuploadAvatar(puppet.DefaultIntent(), avatar.URL)
		if err != nil {
			puppet.log.Warnln("Failed to reupload avatar:", err)
			return false
		}

		puppet.AvatarURL = url
	}

	err = puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warnln("Failed to set avatar:", err)
	}
	puppet.log.Debugln("Updated avatar", puppet.Avatar, "->", avatar.ID)
	puppet.Avatar = avatar.ID
	go puppet.updatePortalAvatar()
	return true
}

func (puppet *Puppet) UpdateName(source *User, contact types.ContactInfo) bool {
	newName, quality := puppet.bridge.Config.Bridge.FormatDisplayname(puppet.JID, contact)
	if puppet.Displayname != newName && quality >= puppet.NameQuality {
		err := puppet.DefaultIntent().SetDisplayName(newName)
		if err == nil {
			puppet.log.Debugln("Updated name", puppet.Displayname, "->", newName)
			puppet.Displayname = newName
			puppet.NameQuality = quality
			go puppet.updatePortalName()
			puppet.Update()
		} else {
			puppet.log.Warnln("Failed to set display name:", err)
		}
		return true
	}
	return false
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	if puppet.bridge.Config.Bridge.PrivateChatPortalMeta {
		for _, portal := range puppet.bridge.GetAllPortalsByJID(puppet.JID) {
			// Get room create lock to prevent races between receiving contact info and room creation.
			portal.roomCreateLock.Lock()
			meta(portal)
			portal.roomCreateLock.Unlock()
		}
	}
}

func (puppet *Puppet) updatePortalAvatar() {
	puppet.updatePortalMeta(func(portal *Portal) {
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, puppet.AvatarURL)
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			}
		}
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.Update(nil)
	})
}

func (puppet *Puppet) updatePortalName() {
	puppet.updatePortalMeta(func(portal *Portal) {
		if len(portal.MXID) > 0 {
			_, err := portal.MainIntent().SetRoomName(portal.MXID, puppet.Displayname)
			if err != nil {
				portal.log.Warnln("Failed to set name:", err)
			}
		}
		portal.Name = puppet.Displayname
		portal.Update(nil)
	})
}

func (puppet *Puppet) SyncContact(source *User, onlyIfNoName, shouldHavePushName bool, reason string) {
	if onlyIfNoName && len(puppet.Displayname) > 0 && (!shouldHavePushName || puppet.NameQuality > config.NameQualityPhone) {
		return
	}

	contact, err := source.Client.Store.Contacts.GetContact(puppet.JID)
	if err != nil {
		puppet.log.Warnfln("Failed to get contact info through %s in SyncContact: %v (sync reason: %s)", source.MXID, reason)
	} else if !contact.Found {
		puppet.log.Warnfln("No contact info found through %s in SyncContact (sync reason: %s)", source.MXID, reason)
	}
	puppet.Sync(source, contact)
}

func (puppet *Puppet) Sync(source *User, contact types.ContactInfo) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()
	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	if puppet.JID.User == source.JID.User {
		contact.PushName = source.Client.Store.PushName
	}

	update := false
	update = puppet.UpdateName(source, contact) || update
	if len(puppet.Avatar) == 0 || puppet.bridge.Config.Bridge.UserAvatarSync {
		update = puppet.UpdateAvatar(source) || update
	}
	if update {
		puppet.Update()
	}
}
