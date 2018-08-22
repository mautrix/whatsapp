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
	"github.com/Rhymen/go-whatsapp"
	"sync"
	"net/http"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

func (user *User) GetPortalByMXID(mxid types.MatrixRoomID) *Portal {
	user.portalsLock.Lock()
	defer user.portalsLock.Unlock()
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
	user.portalsLock.Lock()
	defer user.portalsLock.Unlock()
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
	user.portalsLock.Lock()
	defer user.portalsLock.Unlock()
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

	roomCreateLock sync.Mutex
}

func (portal *Portal) SyncParticipants(metadata *whatsapp_ext.GroupInfo) {
	for _, participant := range metadata.Participants {
		intent := portal.user.GetPuppetByJID(participant.JID).Intent()
		intent.EnsureJoined(portal.MXID)
	}
}

func (portal *Portal) UpdateAvatar() bool {
	avatar, err := portal.user.Conn.GetProfilePicThumb(portal.JID)
	if err != nil {
		portal.log.Errorln(err)
		return false
	}
	if portal.Avatar == avatar.Tag {
		return false
	}

	data, err := avatar.DownloadBytes()
	if err != nil {
		portal.log.Errorln("Failed to download avatar:", err)
		return false
	}

	mime := http.DetectContentType(data)
	resp, err := portal.MainIntent().UploadBytes(data, mime)
	if err != nil {
		portal.log.Errorln("Failed to upload avatar:", err)
		return false
	}

	_, err = portal.MainIntent().SetRoomAvatar(portal.MXID, resp.ContentURI)
	if err != nil {
		portal.log.Warnln("Failed to set room topic:", err)
		return false
	}
	portal.Avatar = avatar.Tag
	return true
}

func (portal *Portal) UpdateName(metadata *whatsapp_ext.GroupInfo) bool {
	if portal.Name != metadata.Name {
		_, err := portal.MainIntent().SetRoomName(portal.MXID, metadata.Name)
		if err == nil {
			portal.Name = metadata.Name
			return true
		}
		portal.log.Warnln("Failed to set room name:", err)
	}
	return false
}

func (portal *Portal) UpdateTopic(metadata *whatsapp_ext.GroupInfo) bool {
	if portal.Topic != metadata.Topic {
		_, err := portal.MainIntent().SetRoomTopic(portal.MXID, metadata.Topic)
		if err == nil {
			portal.Topic = metadata.Topic
			return true
		}
		portal.log.Warnln("Failed to set room topic:", err)
	}
	return false
}

func (portal *Portal) UpdateMetadata() bool {
	metadata, err := portal.user.Conn.GetGroupMetaData(portal.JID)
	if err != nil {
		portal.log.Errorln(err)
		return false
	}
	portal.SyncParticipants(metadata)
	update := false
	update = portal.UpdateName(metadata) || update
	update = portal.UpdateTopic(metadata) || update
	return update
}

func (portal *Portal) Sync(contact whatsapp.Contact) {
	if len(portal.MXID) == 0 {
		if !portal.IsPrivateChat() {
			portal.Name = contact.Name
		}
		err := portal.CreateMatrixRoom()
		if err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return
		}
	}

	if portal.IsPrivateChat() {
		return
	}

	update := false
	update = portal.UpdateMetadata() || update
	update = portal.UpdateAvatar() || update
	if update {
		portal.Update()
	}
}

func (portal *Portal) CreateMatrixRoom() error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if len(portal.MXID) > 0 {
		return nil
	}

	name := portal.Name
	topic := portal.Topic
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

func (portal *Portal) IsDuplicate(id types.WhatsAppMessageID) bool {
	msg := portal.bridge.DB.Message.GetByJID(portal.Owner, id)
	if msg != nil {
		portal.log.Debugln("Ignoring duplicate message", id)
		return true
	}
	return false
}

func (portal *Portal) MarkHandled(jid types.WhatsAppMessageID, mxid types.MatrixEventID) {
	msg := portal.bridge.DB.Message.New()
	msg.Owner = portal.Owner
	msg.JID = jid
	msg.MXID = mxid
	msg.Insert()
}

func (portal *Portal) GetMessageIntent(info whatsapp.MessageInfo) *appservice.IntentAPI {
	if info.FromMe {
		portal.log.Debugln("Unhandled message from me:", info.Id)
		return nil
	} else if portal.IsPrivateChat() {
		return portal.MainIntent()
	}
	puppet := portal.user.GetPuppetByJID(info.SenderJid)
	return puppet.Intent()
}

func (portal *Portal) HandleTextMessage(message whatsapp.TextMessage) {
	if portal.IsDuplicate(message.Info.Id) {
		return
	}

	portal.CreateMatrixRoom()

	intent := portal.GetMessageIntent(message.Info)
	if intent == nil {
		return
	}

	resp, err := intent.SendText(portal.MXID, message.Text)
	if err != nil {
		portal.log.Errorfln("Failed to handle message %s: %v", message.Info.Id, err)
		return
	}
	portal.MarkHandled(message.Info.Id, resp.EventID)
	portal.log.Debugln("Handled message", message.Info.Id, "->", resp.EventID)
}

func (portal *Portal) HandleMediaMessage(download func() ([]byte, error), info whatsapp.MessageInfo, mime, caption string) {
	if portal.IsDuplicate(info.Id) {
		return
	}

	portal.CreateMatrixRoom()

	intent := portal.GetMessageIntent(info)
	if intent == nil {
		return
	}

	img, err := download()
	if err != nil {
		portal.log.Errorln("Failed to download media:", err)
		return
	}
	uploaded, err := intent.UploadBytes(img, mime)
	if err != nil {
		portal.log.Errorln("Failed to upload media:", err)
		return
	}
	resp, err := intent.SendImage(portal.MXID, caption, uploaded.ContentURI)
	if err != nil {
		portal.log.Errorfln("Failed to handle message %s: %v", info.Id, err)
		return
	}
	portal.MarkHandled(info.Id, resp.EventID)
	portal.log.Debugln("Handled message", info.Id, "->", resp.EventID)
}

func (portal *Portal) HandleMatrixMessage(evt *gomatrix.Event) {
	var err error
	switch evt.Content.MsgType {
	case gomatrix.MsgText:
		err = portal.user.Conn.Send(whatsapp.TextMessage{
			Text: evt.Content.Body,
			Info: whatsapp.MessageInfo{
				RemoteJid: portal.JID,
			},
		})
	default:
		portal.log.Debugln("Unhandled Matrix event:", evt)
		return
	}
	if err != nil {
		portal.log.Errorln("Error handling Matrix event %s: %v", evt.ID, err)
	} else {
		portal.log.Debugln("Handled Matrix event:", evt)
	}
}
