// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"net/http"
	"regexp"
	"strings"

	"github.com/Rhymen/go-whatsapp"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

func (bridge *Bridge) ParsePuppetMXID(mxid id.UserID) (types.WhatsAppID, bool) {
	userIDRegex, err := regexp.Compile(fmt.Sprintf("^@%s:%s$",
		bridge.Config.Bridge.FormatUsername("([0-9]+)"),
		bridge.Config.Homeserver.Domain))
	if err != nil {
		bridge.Log.Warnln("Failed to compile puppet user ID regex:", err)
		return "", false
	}
	match := userIDRegex.FindStringSubmatch(string(mxid))
	if match == nil || len(match) != 2 {
		return "", false
	}

	jid := types.WhatsAppID(match[1] + whatsappExt.NewUserSuffix)
	return jid, true
}

func (bridge *Bridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	jid, ok := bridge.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return bridge.GetPuppetByJID(jid)
}

func (bridge *Bridge) GetPuppetByJID(jid types.WhatsAppID) *Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	puppet, ok := bridge.puppets[jid]
	if !ok {
		dbPuppet := bridge.DB.Puppet.Get(jid)
		if dbPuppet == nil {
			dbPuppet = bridge.DB.Puppet.New()
			dbPuppet.JID = jid
			dbPuppet.Insert()
		}
		puppet = bridge.NewPuppet(dbPuppet)
		bridge.puppets[puppet.JID] = puppet
		if len(puppet.CustomMXID) > 0 {
			bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
		}
	}
	return puppet
}

func (bridge *Bridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	puppet, ok := bridge.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet := bridge.DB.Puppet.GetByCustomMXID(mxid)
		if dbPuppet == nil {
			return nil
		}
		puppet = bridge.NewPuppet(dbPuppet)
		bridge.puppets[puppet.JID] = puppet
		bridge.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	return puppet
}

func (bridge *Bridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return bridge.dbPuppetsToPuppets(bridge.DB.Puppet.GetAllWithCustomMXID())
}

func (bridge *Bridge) GetAllPuppets() []*Puppet {
	return bridge.dbPuppetsToPuppets(bridge.DB.Puppet.GetAll())
}

func (bridge *Bridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}
		puppet, ok := bridge.puppets[dbPuppet.JID]
		if !ok {
			puppet = bridge.NewPuppet(dbPuppet)
			bridge.puppets[dbPuppet.JID] = puppet
			if len(dbPuppet.CustomMXID) > 0 {
				bridge.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}
		output[index] = puppet
	}
	return output
}

func (bridge *Bridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.JID)),

		MXID: id.NewUserID(
			bridge.Config.Bridge.FormatUsername(
				strings.Replace(
					dbPuppet.JID,
					whatsappExt.NewUserSuffix, "", 1)),
			bridge.Config.Homeserver.Domain),
	}
}

type Puppet struct {
	*database.Puppet

	bridge *Bridge
	log    log.Logger

	typingIn id.RoomID
	typingAt int64

	MXID id.UserID

	customIntent   *appservice.IntentAPI
	customTypingIn map[id.RoomID]bool
	customUser     *User
}

func (puppet *Puppet) PhoneNumber() string {
	return strings.Replace(puppet.JID, whatsappExt.NewUserSuffix, "", 1)
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if (!portal.IsPrivateChat() && puppet.customIntent == nil) ||
		(portal.backfilling && portal.bridge.Config.Bridge.InviteOwnPuppetForBackfilling) ||
		portal.Key.JID == puppet.JID {
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

func (puppet *Puppet) UpdateAvatar(source *User, avatar *whatsappExt.ProfilePicInfo) bool {
	if avatar == nil {
		var err error
		avatar, err = source.Conn.GetProfilePicThumb(puppet.JID)
		if err != nil {
			puppet.log.Warnln("Failed to get avatar:", err)
			return false
		}
	}

	if avatar.Status != 0 {
		return false
	}

	if avatar.Tag == puppet.Avatar {
		return false
	}

	if len(avatar.URL) == 0 {
		err := puppet.DefaultIntent().SetAvatarURL(id.ContentURI{})
		if err != nil {
			puppet.log.Warnln("Failed to remove avatar:", err)
		}
		puppet.AvatarURL = id.ContentURI{}
		puppet.Avatar = avatar.Tag
		go puppet.updatePortalAvatar()
		return true
	}

	data, err := avatar.DownloadBytes()
	if err != nil {
		puppet.log.Warnln("Failed to download avatar:", err)
		return false
	}

	mime := http.DetectContentType(data)
	resp, err := puppet.DefaultIntent().UploadBytes(data, mime)
	if err != nil {
		puppet.log.Warnln("Failed to upload avatar:", err)
		return false
	}

	puppet.AvatarURL = resp.ContentURI
	err = puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warnln("Failed to set avatar:", err)
	}
	puppet.Avatar = avatar.Tag
	go puppet.updatePortalAvatar()
	return true
}

func (puppet *Puppet) UpdateName(source *User, contact whatsapp.Contact) bool {
	newName, quality := puppet.bridge.Config.Bridge.FormatDisplayname(contact)
	if puppet.Displayname != newName && quality >= puppet.NameQuality {
		err := puppet.DefaultIntent().SetDisplayName(newName)
		if err == nil {
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
			meta(portal)
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
		portal.Update()
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
		portal.Update()
	})
}

func (puppet *Puppet) Sync(source *User, contact whatsapp.Contact) {
	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	if contact.Jid == source.JID {
		contact.Notify = source.Conn.Info.Pushname
	}

	update := false
	update = puppet.UpdateName(source, contact) || update
	update = puppet.UpdateAvatar(source, nil) || update
	if update {
		puppet.Update()
	}
}
