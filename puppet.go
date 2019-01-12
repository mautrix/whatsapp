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
	"net/http"
	"regexp"
	"strings"

	"github.com/Rhymen/go-whatsapp"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix-appservice"

	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

func (bridge *Bridge) ParsePuppetMXID(mxid types.MatrixUserID) (types.WhatsAppID, bool) {
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

func (bridge *Bridge) GetPuppetByMXID(mxid types.MatrixUserID) *Puppet {
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
	}
	return puppet
}

func (bridge *Bridge) GetAllPuppets() []*Puppet {
	bridge.puppetsLock.Lock()
	defer bridge.puppetsLock.Unlock()
	dbPuppets := bridge.DB.Puppet.GetAll()
	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		puppet, ok := bridge.puppets[dbPuppet.JID]
		if !ok {
			puppet = bridge.NewPuppet(dbPuppet)
			bridge.puppets[dbPuppet.JID] = puppet
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

		MXID: fmt.Sprintf("@%s:%s",
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

	typingIn types.MatrixRoomID
	typingAt int64

	MXID types.MatrixUserID
}

func (puppet *Puppet) PhoneNumber() string {
	return strings.Replace(puppet.JID, whatsappExt.NewUserSuffix, "", 1)
}

func (puppet *Puppet) Intent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

func (puppet *Puppet) UpdateAvatar(source *User, avatar *whatsappExt.ProfilePicInfo) bool {
	if avatar == nil {
		var err error
		avatar, err = source.Conn.GetProfilePicThumb(puppet.JID)
		if err != nil {
			puppet.log.Errorln(err)
			return false
		}
	}

	if avatar.Tag == puppet.Avatar {
		return false
	}

	if len(avatar.URL) == 0 {
		puppet.Intent().SetAvatarURL("")
		puppet.Avatar = avatar.Tag
		return true
	}

	data, err := avatar.DownloadBytes()
	if err != nil {
		puppet.log.Errorln("Failed to download avatar:", err)
		return false
	}

	mime := http.DetectContentType(data)
	resp, err := puppet.Intent().UploadBytes(data, mime)
	if err != nil {
		puppet.log.Errorln("Failed to upload avatar:", err)
		return false
	}

	puppet.Intent().SetAvatarURL(resp.ContentURI)
	puppet.Avatar = avatar.Tag
	return true
}

func (puppet *Puppet) Sync(source *User, contact whatsapp.Contact) {
	puppet.Intent().EnsureRegistered()

	if contact.Jid == source.JID {
		contact.Notify = source.Conn.Info.Pushname
	}
	newName, quality := puppet.bridge.Config.Bridge.FormatDisplayname(contact)
	if puppet.Displayname != newName && quality >= puppet.NameQuality {
		err := puppet.Intent().SetDisplayName(newName)
		if err == nil {
			puppet.Displayname = newName
			puppet.NameQuality = quality
			puppet.Update()
		} else {
			puppet.log.Warnln("Failed to set display name:", err)
		}
	}

	if puppet.UpdateAvatar(source, nil) {
		puppet.Update()
	}
}
