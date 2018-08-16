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
	"github.com/Rhymen/go-whatsapp"
	"time"
	"fmt"
	"os"
	"github.com/skip2/go-qrcode"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/types"
)

type User struct {
	*database.User
	Conn *whatsapp.Conn

	bridge *Bridge
	log    log.Logger

	portalsByMXID map[types.MatrixRoomID]*Portal
	portalsByJID  map[types.WhatsAppID]*Portal
	puppets       map[types.WhatsAppID]*Puppet
}

func (bridge *Bridge) GetUser(userID types.MatrixUserID) *User {
	user, ok := bridge.users[userID]
	if !ok {
		dbUser := bridge.DB.User.Get(userID)
		if dbUser == nil {
			dbUser = bridge.DB.User.New()
			dbUser.Insert()
		}
		user = bridge.NewUser(dbUser)
		bridge.users[user.UserID] = user
	}
	return user
}

func (bridge *Bridge) GetAllUsers() []*User {
	dbUsers := bridge.DB.User.GetAll()
	output := make([]*User, len(dbUsers))
	for index, dbUser := range dbUsers {
		user, ok := bridge.users[dbUser.UserID]
		if !ok {
			user = bridge.NewUser(dbUser)
			bridge.users[user.UserID] = user
		}
		output[index] = user
	}
	return output
}

func (bridge *Bridge) InitWhatsApp() {
	users := bridge.GetAllUsers()
	for _, user := range users {
		user.Connect()
	}
}

func (bridge *Bridge) NewUser(dbUser *database.User) *User {
	return &User{
		User:   dbUser,
		bridge: bridge,
		log:    bridge.Log.Sub("User").Sub(dbUser.UserID),
	}
}

func (user *User) Connect() {
	var err error
	user.Conn, err = whatsapp.NewConn(20 * time.Second)
	if err != nil {
		user.log.Errorln("Failed to connect to WhatsApp:", err)
		return
	}
	user.Conn.AddHandler(user)
	user.RestoreSession()
}

func (user *User) RestoreSession() {
	if user.Session != nil {
		sess, err := user.Conn.RestoreSession(*user.Session)
		if err != nil {
			user.log.Errorln("Failed to restore session:", err)
			user.Session = nil
			return
		}
		user.Session = &sess
		user.log.Debugln("Session restored")
	}
	return
}

func (user *User) Login(roomID string) {
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

		bot.SendImage(roomID, string(qrCode), resp.ContentURI)
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

func (user *User) Sync() {
	chats, err := user.Conn.Chats()
	if err != nil {
		user.log.Warnln("Failed to get chats")
		return
	}
	user.log.Debugln(chats)
}

func (user *User) HandleError(err error) {
	user.log.Errorln("WhatsApp error:", err)
	fmt.Fprintf(os.Stderr, "%v", err)
}

func (user *User) HandleTextMessage(message whatsapp.TextMessage) {
	fmt.Println(message)
}

func (user *User) HandleImageMessage(message whatsapp.ImageMessage) {
	fmt.Println(message)
}

func (user *User) HandleVideoMessage(message whatsapp.VideoMessage) {
	fmt.Println(message)
}

func (user *User) HandleJsonMessage(message string) {
	fmt.Println(message)
}
