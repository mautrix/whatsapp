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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Rhymen/go-whatsapp"
	"github.com/skip2/go-qrcode"
	"maunium.net/go/gomatrix/format"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

type User struct {
	*database.User
	Conn *whatsappExt.ExtendedConn

	bridge *Bridge
	log    log.Logger

	Admin       bool
	Whitelisted bool
	jid         string

	portalsByMXID map[types.MatrixRoomID]*Portal
	portalsByJID  map[types.WhatsAppID]*Portal
	portalsLock   sync.Mutex
	puppets       map[types.WhatsAppID]*Puppet
	puppetsLock   sync.Mutex

	htmlParser *format.HTMLParser

	waReplString   map[*regexp.Regexp]string
	waReplFunc     map[*regexp.Regexp]func(string) string
	waReplFuncText map[*regexp.Regexp]func(string) string
}

func (bridge *Bridge) GetUser(userID types.MatrixUserID) *User {
	user, ok := bridge.users[userID]
	if !ok {
		dbUser := bridge.DB.User.Get(userID)
		if dbUser == nil {
			dbUser = bridge.DB.User.New()
			dbUser.ID = userID
			dbUser.Insert()
		}
		user = bridge.NewUser(dbUser)
		bridge.users[user.ID] = user
		if len(user.ManagementRoom) > 0 {
			bridge.managementRooms[user.ManagementRoom] = user
		}
	}
	return user
}

func (bridge *Bridge) GetAllUsers() []*User {
	dbUsers := bridge.DB.User.GetAll()
	output := make([]*User, len(dbUsers))
	for index, dbUser := range dbUsers {
		user, ok := bridge.users[dbUser.ID]
		if !ok {
			user = bridge.NewUser(dbUser)
			bridge.users[user.ID] = user
			if len(user.ManagementRoom) > 0 {
				bridge.managementRooms[user.ManagementRoom] = user
			}
		}
		output[index] = user
	}
	return output
}

func (bridge *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:          dbUser,
		bridge:        bridge,
		log:           bridge.Log.Sub("User").Sub(string(dbUser.ID)),
		portalsByMXID: make(map[types.MatrixRoomID]*Portal),
		portalsByJID:  make(map[types.WhatsAppID]*Portal),
		puppets:       make(map[types.WhatsAppID]*Puppet),
	}
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.ID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.ID)
	user.htmlParser = user.newHTMLParser()
	user.waReplString, user.waReplFunc, user.waReplFuncText = user.newWhatsAppFormatMaps()
	return user
}

func (user *User) SetManagementRoom(roomID types.MatrixRoomID) {
	existingUser, ok := user.bridge.managementRooms[roomID]
	if ok {
		existingUser.ManagementRoom = ""
		existingUser.Update()
	}

	user.ManagementRoom = roomID
	user.bridge.managementRooms[user.ManagementRoom] = user
	user.Update()
}

func (user *User) SetSession(session *whatsapp.Session) {
	user.Session = session
	user.Update()
}

func (user *User) Start() {
	if user.Connect(false) {
		user.Sync()
	}
}

func (user *User) Connect(evenIfNoSession bool) bool {
	if user.Conn != nil {
		return true
	} else if !evenIfNoSession && user.Session == nil {
		return false
	}
	user.log.Debugln("Connecting to WhatsApp")
	conn, err := whatsapp.NewConn(20 * time.Second)
	if err != nil {
		user.log.Errorln("Failed to connect to WhatsApp:", err)
		return false
	}
	user.Conn = whatsappExt.ExtendConn(conn)
	user.log.Debugln("WhatsApp connection successful")
	user.Conn.AddHandler(user)
	return user.RestoreSession()
}

func (user *User) RestoreSession() bool {
	if user.Session != nil {
		sess, err := user.Conn.RestoreSession(*user.Session)
		if err != nil {
			user.log.Errorln("Failed to restore session:", err)
			//user.SetSession(nil)
			return false
		}
		user.SetSession(&sess)
		user.log.Debugln("Session restored successfully")
		return true
	}
	return false
}

func (user *User) Login(roomID types.MatrixRoomID) {
	bot := user.bridge.AppService.BotClient()

	qrChan := make(chan string, 2)
	go func() {
		code := <-qrChan
		if code == "error" {
			return
		}
		qrCode, err := qrcode.Encode(code, qrcode.Low, 256)
		if err != nil {
			user.log.Errorln("Failed to encode QR code:", err)
			bot.SendNotice(roomID, "Failed to encode QR code (see logs for details)")
			return
		}

		resp, err := bot.UploadBytes(qrCode, "image/png")
		if err != nil {
			user.log.Errorln("Failed to upload QR code:", err)
			bot.SendNotice(roomID, "Failed to upload QR code (see logs for details)")
			return
		}

		bot.SendImage(roomID, string(code), resp.ContentURI)
	}()
	session, err := user.Conn.Login(qrChan)
	if err != nil {
		user.log.Warnln("Failed to log in:", err)
		bot.SendNotice(roomID, "Failed to log in: "+err.Error())
		qrChan <- "error"
		return
	}
	user.Session = &session
	user.Update()
	bot.SendNotice(roomID, "Successfully logged in. Synchronizing chats...")
	go user.Sync()
}

func (user *User) JID() string {
	if user.Conn == nil {
		return ""
	}
	if len(user.jid) == 0 {
		user.jid = strings.Replace(user.Conn.Info.Wid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, 1)
	}
	return user.jid
}

func (user *User) Sync() {
	user.log.Debugln("Syncing...")
	user.Conn.Contacts()
	for jid, contact := range user.Conn.Store.Contacts {
		if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
			puppet := user.GetPuppetByJID(contact.Jid)
			puppet.Sync(contact)
		}

		if len(contact.Notify) == 0 && !strings.HasSuffix(jid, "@g.us") {
			// No messages sent -> don't bridge
			continue
		}

		portal := user.GetPortalByJID(contact.Jid)
		portal.Sync(contact)
	}
}

func (user *User) HandleError(err error) {
	user.log.Errorln("WhatsApp error:", err)
}

func (user *User) HandleJSONParseError(err error) {
	user.log.Errorln("WhatsApp JSON parse error:", err)
}

func (user *User) HandleTextMessage(message whatsapp.TextMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleTextMessage(message)
}

func (user *User) HandleImageMessage(message whatsapp.ImageMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(message.Download, message.Thumbnail, message.Info, message.Type, message.Caption)
}

func (user *User) HandleVideoMessage(message whatsapp.VideoMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(message.Download, message.Thumbnail, message.Info, message.Type, message.Caption)
}

func (user *User) HandleAudioMessage(message whatsapp.AudioMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(message.Download, nil, message.Info, message.Type, "")
}

func (user *User) HandleDocumentMessage(message whatsapp.DocumentMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(message.Download, message.Thumbnail, message.Info, message.Type, message.Title)
}

func (user *User) HandlePresence(info whatsappExt.Presence) {
	puppet := user.GetPuppetByJID(info.SenderJID)
	switch info.Status {
	case whatsappExt.PresenceUnavailable:
		puppet.Intent().SetPresence("offline")
	case whatsappExt.PresenceAvailable:
		if len(puppet.typingIn) > 0 && puppet.typingAt+15 > time.Now().Unix() {
			puppet.Intent().UserTyping(puppet.typingIn, false, 0)
			puppet.typingIn = ""
			puppet.typingAt = 0
		} else {
			puppet.Intent().SetPresence("online")
		}
	case whatsappExt.PresenceComposing:
		portal := user.GetPortalByJID(info.JID)
		puppet.typingIn = portal.MXID
		puppet.typingAt = time.Now().Unix()
		puppet.Intent().UserTyping(portal.MXID, true, 15*1000)
	}
}

func (user *User) HandleMsgInfo(info whatsappExt.MsgInfo) {
	if (info.Command == whatsappExt.MsgInfoCommandAck || info.Command == whatsappExt.MsgInfoCommandAcks) && info.Acknowledgement == whatsappExt.AckMessageRead {
		portal := user.GetPortalByJID(info.ToJID)
		if len(portal.MXID) == 0 {
			return
		}

		intent := user.GetPuppetByJID(info.SenderJID).Intent()
		for _, id := range info.IDs {
			msg := user.bridge.DB.Message.GetByJID(user.ID, id)
			if msg == nil {
				continue
			}
			err := intent.MarkRead(portal.MXID, msg.MXID)
			if err != nil {
				user.log.Warnln("Failed to mark message %s as read by %s: %v", msg.MXID, info.SenderJID, err)
			}
		}
	}
}

func (user *User) HandleCommand(cmd whatsappExt.Command) {
	switch cmd.Type {
	case whatsappExt.CommandPicture:
		if strings.HasSuffix(cmd.JID, whatsappExt.NewUserSuffix) {
			puppet := user.GetPuppetByJID(cmd.JID)
			puppet.UpdateAvatar(cmd.ProfilePicInfo)
		} else {
			portal := user.GetPortalByJID(cmd.JID)
			portal.UpdateAvatar(cmd.ProfilePicInfo)
		}
	}
}

func (user *User) HandleChatUpdate(cmd whatsappExt.ChatUpdate) {
	if cmd.Command != whatsappExt.ChatUpdateCommandAction {
		return
	}

	portal := user.GetPortalByJID(cmd.JID)
	if len(portal.MXID) == 0 {
		return
	}

	switch cmd.Data.Action {
	case whatsappExt.ChatActionNameChange:
		portal.UpdateName(cmd.Data.NameChange.Name, cmd.Data.SenderJID)
	case whatsappExt.ChatActionAddTopic:
		portal.UpdateTopic(cmd.Data.AddTopic.Topic, cmd.Data.SenderJID)
	case whatsappExt.ChatActionRemoveTopic:
		portal.UpdateTopic("", cmd.Data.SenderJID)
	case whatsappExt.ChatActionPromote:
		portal.ChangeAdminStatus(cmd.Data.PermissionChange.JIDs, true)
	case whatsappExt.ChatActionDemote:
		portal.ChangeAdminStatus(cmd.Data.PermissionChange.JIDs, false)
	case whatsappExt.ChatActionAnnounce:
		portal.RestrictMessageSending(cmd.Data.Announce)
	case whatsappExt.ChatActionRestrict:
		portal.RestrictMetadataChanges(cmd.Data.Restrict)
	}
}

func (user *User) HandleJsonMessage(message string) {
	user.log.Debugln("JSON message:", message)
}
