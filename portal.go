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
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/gomatrix"
	"strings"
	"maunium.net/go/mautrix-appservice"
)

func (user *User) GetPortalByMXID(mxid types.MatrixRoomID) *Portal {
	portal, ok := user.portalsByMXID[mxid]
	if !ok {
		dbPortal := user.bridge.DB.Portal.GetByMXID(mxid)
		if dbPortal == nil || dbPortal.Owner != user.ID {
			return nil
		}
		portal = user.NewPortal(dbPortal)
		user.portalsByJID[portal.JID] = portal
		if len(portal.MXID) > 0 {
			user.portalsByMXID[portal.MXID] = portal
		}
	}
	return portal
}

func (user *User) GetPortalByJID(jid types.WhatsAppID) *Portal {
	portal, ok := user.portalsByJID[jid]
	if !ok {
		dbPortal := user.bridge.DB.Portal.GetByJID(user.ID, jid)
		if dbPortal == nil {
			dbPortal = user.bridge.DB.Portal.New()
			dbPortal.JID = jid
			dbPortal.Owner = user.ID
			dbPortal.Insert()
		}
		portal = user.NewPortal(dbPortal)
		user.portalsByJID[portal.JID] = portal
		if len(portal.MXID) > 0 {
			user.portalsByMXID[portal.MXID] = portal
		}
	}
	return portal
}

func (user *User) GetAllPortals() []*Portal {
	dbPortals := user.bridge.DB.Portal.GetAll(user.ID)
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		portal, ok := user.portalsByJID[dbPortal.JID]
		if !ok {
			portal = user.NewPortal(dbPortal)
			user.portalsByJID[dbPortal.JID] = portal
			if len(dbPortal.MXID) > 0 {
				user.portalsByMXID[dbPortal.MXID] = portal
			}
		}
		output[index] = portal
	}
	return output
}

func (user *User) NewPortal(dbPortal *database.Portal) *Portal {
	return &Portal{
		Portal: dbPortal,
		user:   user,
		bridge: user.bridge,
		log:    user.log.Sub(fmt.Sprintf("Portal/%s", dbPortal.JID)),
	}
}

type Portal struct {
	*database.Portal

	user   *User
	bridge *Bridge
	log    log.Logger
}

func (portal *Portal) CreateMatrixRoom() error {
	if len(portal.MXID) > 0 {
		return nil
	}

	name := portal.Name
	topic := ""
	isPrivateChat := false
	if strings.HasSuffix(portal.JID, "s.whatsapp.net") {
		puppet := portal.user.GetPuppetByJID(portal.JID)
		name = puppet.Displayname
		topic = "WhatsApp private chat"
		isPrivateChat = true
	}
	resp, err := portal.MainIntent().CreateRoom(&gomatrix.ReqCreateRoom{
		Visibility: "private",
		Name:       name,
		Topic:      topic,
		Invite:     []string{portal.user.ID},
		Preset:     "private_chat",
		IsDirect:   isPrivateChat,
	})
	if err != nil {
		return err
	}
	portal.MXID = resp.RoomID
	portal.Update()
	return nil
}

func (portal *Portal) IsPrivateChat() bool {
	return strings.HasSuffix(portal.JID, puppetJIDStrippedSuffix)
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.user.GetPuppetByJID(portal.JID).Intent()
	}
	return portal.bridge.AppService.BotIntent()
}

func (portal *Portal) HandleMessage(evt *gomatrix.Event) {
	portal.log.Debugln("Received event:", evt)
}
