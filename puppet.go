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
	"maunium.net/go/mautrix-whatsapp/database"
	log "maunium.net/go/maulogger"
	"fmt"
	"regexp"
	"maunium.net/go/mautrix-whatsapp/types"
	"strings"
)

func (bridge *Bridge) ParsePuppetMXID(mxid types.MatrixUserID) (types.MatrixUserID, types.WhatsAppID, bool) {
	userIDRegex, err := regexp.Compile(fmt.Sprintf("^@%s:%s$",
		bridge.Config.Bridge.FormatUsername("([0-9]+)", "([0-9]+)"),
		bridge.Config.Homeserver.Domain))
	if err != nil {
		bridge.Log.Warnln("Failed to compile puppet user ID regex:", err)
		return "", "", false
	}
	match := userIDRegex.FindStringSubmatch(string(mxid))
	if match == nil || len(match) != 3 {
		return "", "", false
	}

	receiver := match[1]
	receiver = strings.Replace(receiver, "=40", "@", 1)
	colonIndex := strings.LastIndex(receiver, "=3")
	receiver = receiver[:colonIndex] + ":" + receiver[colonIndex+len("=3"):]
	return types.MatrixUserID(receiver), types.WhatsAppID(match[2]), true
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
	if !ok || receiver != user.UserID {
		return nil
	}

	return user.GetPuppetByJID(jid)
}

func (user *User) GetPuppetByJID(jid types.WhatsAppID) *Puppet {
	puppet, ok := user.puppets[jid]
	if !ok {
		dbPuppet := user.bridge.DB.Puppet.Get(jid, user.UserID)
		if dbPuppet == nil {
			return nil
		}
		puppet = user.NewPuppet(dbPuppet)
		user.puppets[puppet.JID] = puppet
	}
	return puppet
}

func (user *User) GetAllPuppets() []*Puppet {
	dbPuppets := user.bridge.DB.Puppet.GetAll(user.UserID)
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
	}
}

type Puppet struct {
	*database.Puppet

	user   *User
	bridge *Bridge
	log    log.Logger
}
