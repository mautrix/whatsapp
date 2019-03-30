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
	"encoding/json"
	"strings"
	"time"

	"github.com/Rhymen/go-whatsapp"
	"github.com/skip2/go-qrcode"
	log "maunium.net/go/maulogger/v2"

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
}

func (bridge *Bridge) GetUserByMXID(userID types.MatrixUserID) *User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByMXID[userID]
	if !ok {
		dbUser := bridge.DB.User.GetByMXID(userID)
		if dbUser == nil {
			dbUser = bridge.DB.User.New()
			dbUser.MXID = userID
			dbUser.Insert()
		}
		user = bridge.NewUser(dbUser)
		bridge.usersByMXID[user.MXID] = user
		if len(user.JID) > 0 {
			bridge.usersByJID[user.JID] = user
		}
		if len(user.ManagementRoom) > 0 {
			bridge.managementRooms[user.ManagementRoom] = user
		}
	}
	return user
}

func (bridge *Bridge) GetUserByJID(userID types.WhatsAppID) *User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByJID[userID]
	if !ok {
		dbUser := bridge.DB.User.GetByJID(userID)
		if dbUser == nil {
			return nil
		}
		user = bridge.NewUser(dbUser)
		bridge.usersByMXID[user.MXID] = user
		bridge.usersByJID[user.JID] = user
		if len(user.ManagementRoom) > 0 {
			bridge.managementRooms[user.ManagementRoom] = user
		}
	}
	return user
}

func (bridge *Bridge) GetAllUsers() []*User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	dbUsers := bridge.DB.User.GetAll()
	output := make([]*User, len(dbUsers))
	for index, dbUser := range dbUsers {
		user, ok := bridge.usersByMXID[dbUser.MXID]
		if !ok {
			user = bridge.NewUser(dbUser)
			bridge.usersByMXID[user.MXID] = user
			if len(user.JID) > 0 {
				bridge.usersByJID[user.JID] = user
			}
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
		User:   dbUser,
		bridge: bridge,
		log:    bridge.Log.Sub("User").Sub(string(dbUser.MXID)),
	}
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.MXID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.MXID)
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
	user.Conn.SetClientName("Mautrix-WhatsApp bridge", "mx-wa")
	user.log.Debugln("WhatsApp connection successful")
	user.Conn.AddHandler(user)
	return user.RestoreSession()
}

func (user *User) RestoreSession() bool {
	if user.Session != nil {
		sess, err := user.Conn.RestoreSession(*user.Session)
		if err != nil {
			user.log.Errorln("Failed to restore session:", err)
			return false
		}
		user.SetSession(&sess)
		user.log.Debugln("Session restored successfully")
		return true
	}
	return false
}

func (user *User) IsLoggedIn() bool {
	return user.Conn != nil
}

func (user *User) Login(roomID types.MatrixRoomID) {
	bot := user.bridge.AS.BotClient()

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

		_, err = bot.SendImage(roomID, string(code), resp.ContentURI)
		if err != nil {
			user.log.Errorln("Failed to send QR code to user:", err)
		}
	}()
	session, err := user.Conn.Login(qrChan)
	if err != nil {
		user.log.Warnln("Failed to log in:", err)
		bot.SendNotice(roomID, "Failed to log in: "+err.Error())
		qrChan <- "error"
		return
	}
	user.JID = strings.Replace(user.Conn.Info.Wid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, 1)
	user.Session = &session
	user.Update()
	_, err = bot.SendNotice(roomID, "Successfully logged in. Now, you may ask for `sync [--create]`.")
	if err != nil {
		user.log.Warnln("Failed to send login confirmation to user:", err)
	}
}

func (user *User) HandleError(err error) {
	user.log.Errorln("WhatsApp error:", err)
}

func (user *User) HandleJSONParseError(err error) {
	user.log.Errorln("WhatsApp JSON parse error:", err)
}

func (user *User) PortalKey(jid types.WhatsAppID) database.PortalKey {
	return database.NewPortalKey(jid, user.JID)
}

func (user *User) GetPortalByJID(jid types.WhatsAppID) *Portal {
	return user.bridge.GetPortalByJID(user.PortalKey(jid))
}

func (user *User) HandleTextMessage(message whatsapp.TextMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleTextMessage(user, message)
}

func (user *User) HandleImageMessage(message whatsapp.ImageMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(user, message.Download, message.Thumbnail, message.Info, message.Type, message.Caption)
}

func (user *User) HandleVideoMessage(message whatsapp.VideoMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(user, message.Download, message.Thumbnail, message.Info, message.Type, message.Caption)
}

func (user *User) HandleAudioMessage(message whatsapp.AudioMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(user, message.Download, nil, message.Info, message.Type, "")
}

func (user *User) HandleDocumentMessage(message whatsapp.DocumentMessage) {
	portal := user.GetPortalByJID(message.Info.RemoteJid)
	portal.HandleMediaMessage(user, message.Download, message.Thumbnail, message.Info, message.Type, message.Title)
}

func (user *User) HandlePresence(info whatsappExt.Presence) {
	puppet := user.bridge.GetPuppetByJID(info.SenderJID)
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
		if len(puppet.typingIn) > 0 && puppet.typingAt+15 > time.Now().Unix() {
			if puppet.typingIn == portal.MXID {
				return
			}
			puppet.Intent().UserTyping(puppet.typingIn, false, 0)
		}
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

		intent := user.bridge.GetPuppetByJID(info.SenderJID).Intent()
		for _, id := range info.IDs {
			msg := user.bridge.DB.Message.GetByJID(portal.Key, id)
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
			puppet := user.bridge.GetPuppetByJID(cmd.JID)
			puppet.UpdateAvatar(user, cmd.ProfilePicInfo)
		} else {
			portal := user.GetPortalByJID(cmd.JID)
			portal.UpdateAvatar(user, cmd.ProfilePicInfo)
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
	var msg json.RawMessage
	err := json.Unmarshal([]byte(message), &msg)
	if err != nil {
		return
	}
	user.log.Debugln("JSON message:", message)
}
