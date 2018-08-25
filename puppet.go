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
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Rhymen/go-whatsapp"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

func (bridge *Bridge) ParsePuppetMXID(mxid types.MatrixUserID) (types.MatrixUserID, types.WhatsAppID, bool) {
	userIDRegex, err := regexp.Compile(fmt.Sprintf("^@%s:%s$",
		bridge.Config.Bridge.FormatUsername("(.+)", "([0-9]+)"),
		bridge.Config.Homeserver.Domain))
	if err != nil {
		bridge.Log.Warnln("Failed to compile puppet user ID regex:", err)
		return "", "", false
	}
	match := userIDRegex.FindStringSubmatch(string(mxid))
	if match == nil || len(match) != 3 {
		return "", "", false
	}

	receiver := types.MatrixUserID(match[1])
	receiver = strings.Replace(receiver, "=40", "@", 1)
	colonIndex := strings.LastIndex(receiver, "=3")
	receiver = receiver[:colonIndex] + ":" + receiver[colonIndex+len("=3"):]
	jid := types.WhatsAppID(match[2] + whatsappExt.NewUserSuffix)
	return receiver, jid, true
}

func (bridge *Bridge) GetPuppetByMXID(mxid types.MatrixUserID) *Puppet {
	receiver, jid, ok := bridge.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	user := bridge.GetUser(receiver)
	if user == nil {
		return nil
	}

	return user.GetPuppetByJID(jid)
}

func (user *User) GetPuppetByMXID(mxid types.MatrixUserID) *Puppet {
	receiver, jid, ok := user.bridge.ParsePuppetMXID(mxid)
	if !ok || receiver != user.ID {
		return nil
	}

	return user.GetPuppetByJID(jid)
}

func (user *User) GetPuppetByJID(jid types.WhatsAppID) *Puppet {
	user.puppetsLock.Lock()
	defer user.puppetsLock.Unlock()
	puppet, ok := user.puppets[jid]
	if !ok {
		dbPuppet := user.bridge.DB.Puppet.Get(jid, user.ID)
		if dbPuppet == nil {
			dbPuppet = user.bridge.DB.Puppet.New()
			dbPuppet.JID = jid
			dbPuppet.Receiver = user.ID
			dbPuppet.Insert()
		}
		puppet = user.NewPuppet(dbPuppet)
		user.puppets[puppet.JID] = puppet
	}
	return puppet
}

func (user *User) GetAllPuppets() []*Puppet {
	user.puppetsLock.Lock()
	defer user.puppetsLock.Unlock()
	dbPuppets := user.bridge.DB.Puppet.GetAll(user.ID)
	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		puppet, ok := user.puppets[dbPuppet.JID]
		if !ok {
			puppet = user.NewPuppet(dbPuppet)
			user.puppets[dbPuppet.JID] = puppet
		}
		output[index] = puppet
	}
	return output
}

func (user *User) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		user:   user,
		bridge: user.bridge,
		log:    user.log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.JID)),

		MXID: fmt.Sprintf("@%s:%s",
			user.bridge.Config.Bridge.FormatUsername(
				dbPuppet.Receiver,
				strings.Replace(
					dbPuppet.JID,
					whatsappExt.NewUserSuffix, "", 1)),
			user.bridge.Config.Homeserver.Domain),
	}
}

type Puppet struct {
	*database.Puppet

	user   *User
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
	return puppet.bridge.AppService.Intent(puppet.MXID)
}

func (puppet *Puppet) UpdateAvatar(avatar *whatsappExt.ProfilePicInfo) bool {
	if avatar == nil {
		var err error
		avatar, err = puppet.user.Conn.GetProfilePicThumb(puppet.JID)
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

func (puppet *Puppet) Sync(contact whatsapp.Contact) {
	puppet.Intent().EnsureRegistered()

	newName := puppet.bridge.Config.Bridge.FormatDisplayname(contact)
	if puppet.Displayname != newName {
		err := puppet.Intent().SetDisplayName(newName)
		if err == nil {
			puppet.Displayname = newName
			puppet.Update()
		} else {
			puppet.log.Warnln("Failed to set display name:", err)
		}
	}

	if puppet.UpdateAvatar(nil) {
		puppet.Update()
	}
}
