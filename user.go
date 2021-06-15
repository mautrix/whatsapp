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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skip2/go-qrcode"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/pushrules"

	"github.com/Rhymen/go-whatsapp"
	waBinary "github.com/Rhymen/go-whatsapp/binary"
	waProto "github.com/Rhymen/go-whatsapp/binary/proto"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

type User struct {
	*database.User
	Conn *whatsapp.Conn

	bridge *Bridge
	log    log.Logger

	Admin               bool
	Whitelisted         bool
	RelaybotWhitelisted bool

	IsRelaybot bool

	ConnectionErrors int
	CommunityID      string

	cleanDisconnection  bool
	batteryWarningsSent int
	lastReconnection    int64
	pushName            string

	chatListReceived chan struct{}
	syncPortalsDone  chan struct{}

	messageInput  chan PortalMessage
	messageOutput chan PortalMessage

	syncStart chan struct{}
	syncWait  sync.WaitGroup
	syncing   int32

	mgmtCreateLock  sync.Mutex
	connLock        sync.Mutex
	cancelReconnect func()

	prevBridgeStatus *BridgeState
}

func (bridge *Bridge) GetUserByMXID(userID id.UserID) *User {
	_, isPuppet := bridge.ParsePuppetMXID(userID)
	if isPuppet || userID == bridge.Bot.UserID {
		return nil
	}
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByMXID[userID]
	if !ok {
		return bridge.loadDBUser(bridge.DB.User.GetByMXID(userID), &userID)
	}
	return user
}

func (bridge *Bridge) GetUserByJID(userID whatsapp.JID) *User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByJID[userID]
	if !ok {
		return bridge.loadDBUser(bridge.DB.User.GetByJID(userID), nil)
	}
	return user
}

func (user *User) addToJIDMap() {
	user.bridge.usersLock.Lock()
	user.bridge.usersByJID[user.JID] = user
	user.bridge.usersLock.Unlock()
}

func (user *User) removeFromJIDMap() {
	user.bridge.usersLock.Lock()
	jidUser, ok := user.bridge.usersByJID[user.JID]
	if ok && user == jidUser {
		delete(user.bridge.usersByJID, user.JID)
	}
	user.bridge.usersLock.Unlock()
	user.bridge.Metrics.TrackLoginState(user.JID, false)
	user.sendBridgeState(BridgeState{Error: WANotLoggedIn})
}

func (bridge *Bridge) GetAllUsers() []*User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	dbUsers := bridge.DB.User.GetAll()
	output := make([]*User, len(dbUsers))
	for index, dbUser := range dbUsers {
		user, ok := bridge.usersByMXID[dbUser.MXID]
		if !ok {
			user = bridge.loadDBUser(dbUser, nil)
		}
		output[index] = user
	}
	return output
}

func (bridge *Bridge) loadDBUser(dbUser *database.User, mxid *id.UserID) *User {
	if dbUser == nil {
		if mxid == nil {
			return nil
		}
		dbUser = bridge.DB.User.New()
		dbUser.MXID = *mxid
		dbUser.Insert()
	}
	user := bridge.NewUser(dbUser)
	bridge.usersByMXID[user.MXID] = user
	if len(user.JID) > 0 {
		bridge.usersByJID[user.JID] = user
	}
	if len(user.ManagementRoom) > 0 {
		bridge.managementRooms[user.ManagementRoom] = user
	}
	return user
}

func (user *User) GetPortals() []*Portal {
	keys := user.User.GetPortalKeys()
	portals := make([]*Portal, len(keys))

	user.bridge.portalsLock.Lock()
	for i, key := range keys {
		portal, ok := user.bridge.portalsByJID[key]
		if !ok {
			portal = user.bridge.loadDBPortal(user.bridge.DB.Portal.GetByJID(key), &key)
		}
		portals[i] = portal
	}
	user.bridge.portalsLock.Unlock()
	return portals
}

func (bridge *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: bridge,
		log:    bridge.Log.Sub("User").Sub(string(dbUser.MXID)),

		IsRelaybot: false,

		chatListReceived: make(chan struct{}, 1),
		syncPortalsDone:  make(chan struct{}, 1),
		syncStart:        make(chan struct{}, 1),
		messageInput:     make(chan PortalMessage),
		messageOutput:    make(chan PortalMessage, bridge.Config.Bridge.UserMessageBuffer),
	}
	user.RelaybotWhitelisted = user.bridge.Config.Bridge.Permissions.IsRelaybotWhitelisted(user.MXID)
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.MXID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.MXID)
	go user.handleMessageLoop()
	go user.runMessageRingBuffer()
	return user
}

func (user *User) GetManagementRoom() id.RoomID {
	if len(user.ManagementRoom) == 0 {
		user.mgmtCreateLock.Lock()
		defer user.mgmtCreateLock.Unlock()
		if len(user.ManagementRoom) > 0 {
			return user.ManagementRoom
		}
		resp, err := user.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Topic:    "WhatsApp bridge notices",
			IsDirect: true,
		})
		if err != nil {
			user.log.Errorln("Failed to auto-create management room:", err)
		} else {
			user.SetManagementRoom(resp.RoomID)
		}
	}
	return user.ManagementRoom
}

func (user *User) SetManagementRoom(roomID id.RoomID) {
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
	if session == nil {
		user.Session = nil
		user.LastConnection = 0
	} else if len(session.Wid) > 0 {
		user.Session = session
	} else {
		return
	}
	user.Update()
}

func (user *User) Connect(evenIfNoSession bool) bool {
	user.connLock.Lock()
	if user.Conn != nil {
		user.connLock.Unlock()
		if user.Conn.IsConnected() {
			return true
		} else {
			return user.RestoreSession()
		}
	} else if !evenIfNoSession && user.Session == nil {
		user.connLock.Unlock()
		return false
	}
	user.log.Debugln("Connecting to WhatsApp")
	if user.Session != nil {
		user.sendBridgeState(BridgeState{Error: WAConnecting})
	}
	timeout := time.Duration(user.bridge.Config.Bridge.ConnectionTimeout)
	if timeout == 0 {
		timeout = 20
	}
	user.Conn = whatsapp.NewConn(&whatsapp.Options{
		Timeout:         timeout * time.Second,
		LongClientName:  user.bridge.Config.WhatsApp.OSName,
		ShortClientName: user.bridge.Config.WhatsApp.BrowserName,
		ClientVersion:   WAVersion,
		Log:             user.log.Sub("Conn"),
		Handler:         []whatsapp.Handler{user},
	})
	user.setupAdminTestHooks()
	user.connLock.Unlock()
	return user.RestoreSession()
}

func (user *User) DeleteConnection() {
	user.connLock.Lock()
	if user.Conn == nil {
		user.connLock.Unlock()
		return
	}
	err := user.Conn.Disconnect()
	if err != nil && err != whatsapp.ErrNotConnected {
		user.log.Warnln("Error disconnecting: %v", err)
	}
	user.Conn.RemoveHandlers()
	user.Conn = nil
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	user.sendBridgeState(BridgeState{Error: WANotConnected})
	user.connLock.Unlock()
}

func (user *User) RestoreSession() bool {
	if user.Session != nil {
		user.Conn.SetSession(*user.Session)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		err := user.Conn.Restore(true, ctx)
		if err == whatsapp.ErrAlreadyLoggedIn {
			return true
		} else if err != nil {
			user.log.Errorln("Failed to restore session:", err)
			if errors.Is(err, whatsapp.ErrUnpaired) {
				user.sendMarkdownBridgeAlert("\u26a0 Failed to connect to WhatsApp: unpaired from phone. " +
					"To re-pair your phone, log in again.")
				user.removeFromJIDMap()
				//user.JID = ""
				user.SetSession(nil)
				user.DeleteConnection()
				return false
			} else {
				user.sendBridgeState(BridgeState{Error: WANotConnected})
				user.sendMarkdownBridgeAlert("\u26a0 Failed to connect to WhatsApp. Make sure WhatsApp " +
					"on your phone is reachable and use `reconnect` to try connecting again.")
			}
			user.log.Debugln("Disconnecting due to failed session restore...")
			err = user.Conn.Disconnect()
			if err != nil {
				user.log.Errorln("Failed to disconnect after failed session restore:", err)
			}
			return false
		}
		user.ConnectionErrors = 0
		user.log.Debugln("Session restored successfully")
		user.PostLogin()
	}
	return true
}

func (user *User) HasSession() bool {
	return user.Session != nil
}

func (user *User) IsConnected() bool {
	return user.Conn != nil && user.Conn.IsConnected() && user.Conn.IsLoggedIn()
}

func (user *User) IsLoginInProgress() bool {
	return user.Conn != nil && user.Conn.IsLoginInProgress()
}

func (user *User) loginQrChannel(ce *CommandEvent, qrChan <-chan string, eventIDChan chan<- id.EventID) {
	var qrEventID id.EventID
	for code := range qrChan {
		if code == "stop" {
			return
		}
		qrCode, err := qrcode.Encode(code, qrcode.Low, 256)
		if err != nil {
			user.log.Errorln("Failed to encode QR code:", err)
			ce.Reply("Failed to encode QR code: %v", err)
			return
		}

		bot := user.bridge.AS.BotClient()

		resp, err := bot.UploadBytes(qrCode, "image/png")
		if err != nil {
			user.log.Errorln("Failed to upload QR code:", err)
			ce.Reply("Failed to upload QR code: %v", err)
			return
		}

		if qrEventID == "" {
			sendResp, err := bot.SendImage(ce.RoomID, code, resp.ContentURI)
			if err != nil {
				user.log.Errorln("Failed to send QR code to user:", err)
				return
			}
			qrEventID = sendResp.EventID
			eventIDChan <- qrEventID
		} else {
			_, err = bot.SendMessageEvent(ce.RoomID, event.EventMessage, &event.MessageEventContent{
				MsgType: event.MsgImage,
				Body:    code,
				URL:     resp.ContentURI.CUString(),
				NewContent: &event.MessageEventContent{
					MsgType: event.MsgImage,
					Body:    code,
					URL:     resp.ContentURI.CUString(),
				},
				RelatesTo: &event.RelatesTo{
					Type:    event.RelReplace,
					EventID: qrEventID,
				},
			})
			if err != nil {
				user.log.Errorln("Failed to send edited QR code to user:", err)
			}
		}
	}
}

func (user *User) Login(ce *CommandEvent) {
	qrChan := make(chan string, 3)
	eventIDChan := make(chan id.EventID, 1)
	go user.loginQrChannel(ce, qrChan, eventIDChan)
	session, jid, err := user.Conn.Login(qrChan, nil)
	qrChan <- "stop"
	if err != nil {
		var eventID id.EventID
		select {
		case eventID = <-eventIDChan:
		default:
		}
		reply := event.MessageEventContent{
			MsgType: event.MsgText,
		}
		if err == whatsapp.ErrAlreadyLoggedIn {
			reply.Body = "You're already logged in"
		} else if err == whatsapp.ErrLoginInProgress {
			reply.Body = "You have a login in progress already."
		} else if err == whatsapp.ErrLoginTimedOut {
			reply.Body = "QR code scan timed out. Please try again."
		} else {
			user.log.Warnln("Failed to log in:", err)
			reply.Body = fmt.Sprintf("Unknown error while logging in: %v", err)
		}
		msg := reply
		if eventID != "" {
			msg.NewContent = &reply
			msg.RelatesTo = &event.RelatesTo{
				Type:    event.RelReplace,
				EventID: eventID,
			}
		}
		_, _ = ce.Bot.SendMessageEvent(ce.RoomID, event.EventMessage, &msg)
		return
	}
	// TODO there's a bit of duplication between this and the provisioning API login method
	//      Also between the two logout methods (commands.go and provisioning.go)
	user.log.Debugln("Successful login as", jid, "via command")
	user.ConnectionErrors = 0
	user.JID = strings.Replace(jid, whatsapp.OldUserSuffix, whatsapp.NewUserSuffix, 1)
	user.addToJIDMap()
	user.SetSession(&session)
	ce.Reply("Successfully logged in, synchronizing chats...")
	user.PostLogin()
}

type Chat struct {
	whatsapp.Chat
	Portal  *Portal
	Contact whatsapp.Contact
}

type ChatList []Chat

func (cl ChatList) Len() int {
	return len(cl)
}

func (cl ChatList) Less(i, j int) bool {
	return cl[i].LastMessageTime > cl[j].LastMessageTime
}

func (cl ChatList) Swap(i, j int) {
	cl[i], cl[j] = cl[j], cl[i]
}

func (user *User) PostLogin() {
	user.sendBridgeState(BridgeState{OK: true})
	user.bridge.Metrics.TrackConnectionState(user.JID, true)
	user.bridge.Metrics.TrackLoginState(user.JID, true)
	user.bridge.Metrics.TrackBufferLength(user.MXID, len(user.messageOutput))
	if !atomic.CompareAndSwapInt32(&user.syncing, 0, 1) {
		// TODO we should cleanly stop the old sync and start a new one instead of not starting a new one
		user.log.Warnln("There seems to be a post-sync already in progress, not starting a new one")
		return
	}
	user.log.Debugln("Locking processing of incoming messages and starting post-login sync")
	user.chatListReceived = make(chan struct{}, 1)
	user.syncPortalsDone = make(chan struct{}, 1)
	user.syncWait.Add(1)
	user.syncStart <- struct{}{}
	go user.intPostLogin()
}

func (user *User) tryAutomaticDoublePuppeting() {
	if !user.bridge.Config.CanDoublePuppet(user.MXID) {
		return
	}
	user.log.Debugln("Checking if double puppeting needs to be enabled")
	puppet := user.bridge.GetPuppetByJID(user.JID)
	if len(puppet.CustomMXID) > 0 {
		user.log.Debugln("User already has double-puppeting enabled")
		// Custom puppet already enabled
		return
	}
	accessToken, err := puppet.loginWithSharedSecret(user.MXID)
	if err != nil {
		user.log.Warnln("Failed to login with shared secret:", err)
		return
	}
	err = puppet.SwitchCustomMXID(accessToken, user.MXID)
	if err != nil {
		puppet.log.Warnln("Failed to switch to auto-logined custom puppet:", err)
		return
	}
	user.log.Infoln("Successfully automatically enabled custom puppet")
}

func (user *User) sendBridgeNotice(formatString string, args ...interface{}) {
	notice := fmt.Sprintf(formatString, args...)
	_, err := user.bridge.Bot.SendNotice(user.GetManagementRoom(), notice)
	if err != nil {
		user.log.Warnf("Failed to send bridge notice \"%s\": %v", notice, err)
	}
}

func (user *User) sendMarkdownBridgeAlert(formatString string, args ...interface{}) {
	notice := fmt.Sprintf(formatString, args...)
	content := format.RenderMarkdown(notice, true, false)
	_, err := user.bridge.Bot.SendMessageEvent(user.GetManagementRoom(), event.EventMessage, content)
	if err != nil {
		user.log.Warnf("Failed to send bridge alert \"%s\": %v", notice, err)
	}
}

func (user *User) postConnPing() bool {
	user.log.Debugln("Making post-connection ping")
	var err error
	for i := 0; ; i++ {
		err = user.Conn.AdminTest()
		if err == nil {
			user.log.Debugln("Post-connection ping OK")
			return true
		} else if errors.Is(err, whatsapp.ErrConnectionTimeout) && i < 5 {
			user.log.Warnfln("Post-connection ping timed out, sending new one")
		} else {
			break
		}
	}
	user.log.Errorfln("Post-connection ping failed: %v. Disconnecting and then reconnecting after a second", err)
	disconnectErr := user.Conn.Disconnect()
	if disconnectErr != nil {
		user.log.Warnln("Error while disconnecting after failed post-connection ping:", disconnectErr)
	}
	user.sendBridgeState(BridgeState{Error: WANotConnected})
	user.bridge.Metrics.TrackDisconnection(user.MXID)
	go func() {
		time.Sleep(1 * time.Second)
		user.tryReconnect(fmt.Sprintf("Post-connection ping failed: %v", err))
	}()
	return false
}

func (user *User) intPostLogin() {
	defer atomic.StoreInt32(&user.syncing, 0)
	defer user.syncWait.Done()
	user.lastReconnection = time.Now().Unix()
	user.createCommunity()
	user.tryAutomaticDoublePuppeting()

	user.log.Debugln("Waiting for chat list receive confirmation")
	select {
	case <-user.chatListReceived:
		user.log.Debugln("Chat list receive confirmation received in PostLogin")
	case <-time.After(time.Duration(user.bridge.Config.Bridge.ChatListWait) * time.Second):
		user.log.Warnln("Timed out waiting for chat list to arrive!")
		user.postConnPing()
		return
	}

	if !user.postConnPing() {
		user.log.Debugln("Post-connection ping failed, unlocking processing of incoming messages.")
		return
	}

	user.log.Debugln("Waiting for portal sync complete confirmation")
	select {
	case <-user.syncPortalsDone:
		user.log.Debugln("Post-connection portal sync complete, unlocking processing of incoming messages.")
	// TODO this is too short, maybe a per-portal duration?
	case <-time.After(time.Duration(user.bridge.Config.Bridge.PortalSyncWait) * time.Second):
		user.log.Warnln("Timed out waiting for portal sync to complete! Unlocking processing of incoming messages.")
	}
}

type NormalMessage interface {
	GetInfo() whatsapp.MessageInfo
}

func (user *User) HandleEvent(event interface{}) {
	switch v := event.(type) {
	case NormalMessage:
		info := v.GetInfo()
		user.messageInput <- PortalMessage{info.RemoteJid, user, v, info.Timestamp}
	case whatsapp.MessageRevocation:
		user.messageInput <- PortalMessage{v.RemoteJid, user, v, 0}
	case whatsapp.StreamEvent:
		user.HandleStreamEvent(v)
	case []whatsapp.Chat:
		user.HandleChatList(v)
	case []whatsapp.Contact:
		user.HandleContactList(v)
	case error:
		user.HandleError(v)
	case whatsapp.Contact:
		go user.HandleNewContact(v)
	case whatsapp.BatteryMessage:
		user.HandleBatteryMessage(v)
	case whatsapp.CallInfo:
		user.HandleCallInfo(v)
	case whatsapp.PresenceEvent:
		go user.HandlePresence(v)
	case whatsapp.JSONMsgInfo:
		go user.HandleMsgInfo(v)
	case whatsapp.ReceivedMessage:
		user.HandleReceivedMessage(v)
	case whatsapp.ReadMessage:
		user.HandleReadMessage(v)
	case whatsapp.JSONCommand:
		user.HandleCommand(v)
	case whatsapp.ChatUpdate:
		user.HandleChatUpdate(v)
	case whatsapp.ConnInfo:
		user.HandleConnInfo(v)
	case whatsapp.MuteMessage:
		portal := user.bridge.GetPortalByJID(user.PortalKey(v.JID))
		if portal != nil {
			go user.updateChatMute(nil, portal, v.MutedUntil)
		}
	case whatsapp.ArchiveMessage:
		portal := user.bridge.GetPortalByJID(user.PortalKey(v.JID))
		if portal != nil {
			go user.updateChatTag(nil, portal, user.bridge.Config.Bridge.ArchiveTag, v.IsArchived)
		}
	case whatsapp.PinMessage:
		portal := user.bridge.GetPortalByJID(user.PortalKey(v.JID))
		if portal != nil {
			go user.updateChatTag(nil, portal, user.bridge.Config.Bridge.PinnedTag, v.IsPinned)
		}
	case whatsapp.RawJSONMessage:
		user.HandleJSONMessage(v)
	case *waProto.WebMessageInfo:
		user.updateLastConnectionIfNecessary()
		// TODO trace log
		//user.log.Debugfln("WebMessageInfo: %+v", v)
	case *waBinary.Node:
		user.log.Debugfln("Unknown binary message: %+v", v)
	default:
		user.log.Debugfln("Unknown type of event in HandleEvent: %T", v)
	}
}

func (user *User) HandleStreamEvent(evt whatsapp.StreamEvent) {
	if evt.Type == whatsapp.StreamSleep {
		if user.lastReconnection+60 > time.Now().Unix() {
			user.lastReconnection = 0
			user.log.Infoln("Stream went to sleep soon after reconnection, making new post-connection ping in 20 seconds")
			go func() {
				time.Sleep(20 * time.Second)
				// TODO if this happens during the post-login sync, it can get stuck forever
				// TODO check if the above is still true
				user.postConnPing()
			}()
		}
	} else {
		user.log.Infofln("Stream event: %+v", evt)
	}
}

func (user *User) HandleChatList(chats []whatsapp.Chat) {
	user.log.Infoln("Chat list received")
	chatMap := make(map[string]whatsapp.Chat)
	for _, chat := range user.Conn.Store.Chats {
		chatMap[chat.JID] = chat
	}
	for _, chat := range chats {
		chatMap[chat.JID] = chat
	}
	select {
	case user.chatListReceived <- struct{}{}:
	default:
	}
	go user.syncPortals(chatMap, false)
}

func (user *User) updateChatMute(intent *appservice.IntentAPI, portal *Portal, mutedUntil int64) {
	if len(portal.MXID) == 0 || !user.bridge.Config.Bridge.MuteBridging {
		return
	} else if intent == nil {
		doublePuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
		if doublePuppet == nil || doublePuppet.CustomIntent() == nil {
			return
		}
		intent = doublePuppet.CustomIntent()
	}
	var err error
	if mutedUntil != -1 && mutedUntil < time.Now().Unix() {
		user.log.Debugfln("Portal %s is muted until %d, unmuting...", portal.MXID, mutedUntil)
		err = intent.DeletePushRule("global", pushrules.RoomRule, string(portal.MXID))
	} else {
		user.log.Debugfln("Portal %s is muted until %d, muting...", portal.MXID, mutedUntil)
		err = intent.PutPushRule("global", pushrules.RoomRule, string(portal.MXID), &mautrix.ReqPutPushRule{
			Actions: []pushrules.PushActionType{pushrules.ActionDontNotify},
		})
	}
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		user.log.Warnfln("Failed to update push rule for %s through double puppet: %v", portal.MXID, err)
	}
}

type CustomTagData struct {
	Order        json.Number `json:"order"`
	DoublePuppet bool        `json:"net.maunium.whatsapp.puppet"`
}

type CustomTagEventContent struct {
	Tags map[string]CustomTagData `json:"tags"`
}

func (user *User) updateChatTag(intent *appservice.IntentAPI, portal *Portal, tag string, active bool) {
	if len(portal.MXID) == 0 || len(tag) == 0 {
		return
	} else if intent == nil {
		doublePuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
		if doublePuppet == nil || doublePuppet.CustomIntent() == nil {
			return
		}
		intent = doublePuppet.CustomIntent()
	}
	var existingTags CustomTagEventContent
	err := intent.GetTagsWithCustomData(portal.MXID, &existingTags)
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		user.log.Warnfln("Failed to get tags of %s: %v", portal.MXID, err)
	}
	currentTag, ok := existingTags.Tags[tag]
	if active && !ok {
		user.log.Debugln("Adding tag", tag, "to", portal.MXID)
		data := CustomTagData{"0.5", true}
		err = intent.AddTagWithCustomData(portal.MXID, tag, &data)
	} else if !active && ok && currentTag.DoublePuppet {
		user.log.Debugln("Removing tag", tag, "from", portal.MXID)
		err = intent.RemoveTag(portal.MXID, tag)
	} else {
		err = nil
	}
	if err != nil {
		user.log.Warnfln("Failed to update tag %s for %s through double puppet: %v", tag, portal.MXID, err)
	}
}

type CustomReadReceipt struct {
	Timestamp    int64 `json:"ts,omitempty"`
	DoublePuppet bool  `json:"net.maunium.whatsapp.puppet,omitempty"`
}

func (user *User) syncChatDoublePuppetDetails(doublePuppet *Puppet, chat Chat, justCreated bool) {
	if doublePuppet == nil || doublePuppet.CustomIntent() == nil || len(chat.Portal.MXID) == 0 {
		return
	}
	intent := doublePuppet.CustomIntent()
	if chat.UnreadCount == 0 && (justCreated || !user.bridge.Config.Bridge.MarkReadOnlyOnCreate) {
		lastMessage := user.bridge.DB.Message.GetLastInChatBefore(chat.Portal.Key, chat.ReceivedAt.Unix())
		if lastMessage != nil {
			err := intent.MarkReadWithContent(chat.Portal.MXID, lastMessage.MXID, &CustomReadReceipt{DoublePuppet: true})
			if err != nil {
				user.log.Warnfln("Failed to mark %s in %s as read after backfill: %v", lastMessage.MXID, chat.Portal.MXID, err)
			}
		}
	} else if chat.UnreadCount == -1 {
		user.log.Debugfln("Invalid unread count (missing field?) in chat info %+v", chat.Source)
	}
	if justCreated || !user.bridge.Config.Bridge.TagOnlyOnCreate {
		user.updateChatMute(intent, chat.Portal, chat.MutedUntil)
		user.updateChatTag(intent, chat.Portal, user.bridge.Config.Bridge.ArchiveTag, chat.IsArchived)
		user.updateChatTag(intent, chat.Portal, user.bridge.Config.Bridge.PinnedTag, chat.IsPinned)
	}
}

func (user *User) syncPortal(chat Chat) {
	// Don't sync unless chat meta sync is enabled or portal doesn't exist
	if user.bridge.Config.Bridge.ChatMetaSync || len(chat.Portal.MXID) == 0 {
		failedToCreate := chat.Portal.Sync(user, chat.Contact)
		if failedToCreate {
			return
		}
	}
	err := chat.Portal.BackfillHistory(user, chat.LastMessageTime)
	if err != nil {
		chat.Portal.log.Errorln("Error backfilling history:", err)
	}
}

func (user *User) collectChatList(chatMap map[string]whatsapp.Chat) ChatList {
	if chatMap == nil {
		chatMap = user.Conn.Store.Chats
	}
	user.log.Infoln("Reading chat list")
	chats := make(ChatList, 0, len(chatMap))
	existingKeys := user.GetInCommunityMap()
	portalKeys := make([]database.PortalKeyWithMeta, 0, len(chatMap))
	for _, chat := range chatMap {
		portal := user.GetPortalByJID(chat.JID)

		chats = append(chats, Chat{
			Chat:    chat,
			Portal:  portal,
			Contact: user.Conn.Store.Contacts[chat.JID],
		})
		var inCommunity, ok bool
		if inCommunity, ok = existingKeys[portal.Key]; !ok || !inCommunity {
			inCommunity = user.addPortalToCommunity(portal)
			if portal.IsPrivateChat() {
				puppet := user.bridge.GetPuppetByJID(portal.Key.JID)
				user.addPuppetToCommunity(puppet)
			}
		}
		portalKeys = append(portalKeys, database.PortalKeyWithMeta{PortalKey: portal.Key, InCommunity: inCommunity})
	}
	user.log.Infoln("Read chat list, updating user-portal mapping")
	err := user.SetPortalKeys(portalKeys)
	if err != nil {
		user.log.Warnln("Failed to update user-portal mapping:", err)
	}
	sort.Sort(chats)
	return chats
}

func (user *User) syncPortals(chatMap map[string]whatsapp.Chat, createAll bool) {
	// TODO use contexts instead of checking if user.Conn is the same?
	connAtStart := user.Conn

	chats := user.collectChatList(chatMap)

	limit := user.bridge.Config.Bridge.InitialChatSync
	if limit < 0 {
		limit = len(chats)
	}
	if user.Conn != connAtStart {
		user.log.Debugln("Connection seems to have changed before sync, cancelling")
		return
	}
	now := time.Now().Unix()
	user.log.Infoln("Syncing portals")
	doublePuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	for i, chat := range chats {
		if chat.LastMessageTime+user.bridge.Config.Bridge.SyncChatMaxAge < now {
			break
		}
		create := (chat.LastMessageTime >= user.LastConnection && user.LastConnection > 0) || i < limit
		if len(chat.Portal.MXID) > 0 || create || createAll {
			user.log.Debugfln("Syncing chat %+v", chat.Chat.Source)
			justCreated := len(chat.Portal.MXID) == 0
			user.syncPortal(chat)
			user.syncChatDoublePuppetDetails(doublePuppet, chat, justCreated)
		}
	}
	if user.Conn != connAtStart {
		user.log.Debugln("Connection seems to have changed during sync, cancelling")
		return
	}
	user.UpdateDirectChats(nil)

	user.log.Infoln("Finished syncing portals")
	select {
	case user.syncPortalsDone <- struct{}{}:
	default:
	}
}

func (user *User) getDirectChats() map[id.UserID][]id.RoomID {
	res := make(map[id.UserID][]id.RoomID)
	privateChats := user.bridge.DB.Portal.FindPrivateChats(user.JID)
	for _, portal := range privateChats {
		if len(portal.MXID) > 0 {
			res[user.bridge.FormatPuppetMXID(portal.Key.JID)] = []id.RoomID{portal.MXID}
		}
	}
	return res
}

func (user *User) UpdateDirectChats(chats map[id.UserID][]id.RoomID) {
	if !user.bridge.Config.Bridge.SyncDirectChatList {
		return
	}
	puppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if puppet == nil || puppet.CustomIntent() == nil {
		return
	}
	intent := puppet.CustomIntent()
	method := http.MethodPatch
	if chats == nil {
		chats = user.getDirectChats()
		method = http.MethodPut
	}
	user.log.Debugln("Updating m.direct list on homeserver")
	var err error
	if user.bridge.Config.Homeserver.Asmux {
		urlPath := intent.BuildBaseURL("_matrix", "client", "unstable", "com.beeper.asmux", "dms")
		_, err = intent.MakeFullRequest(mautrix.FullRequest{
			Method:      method,
			URL:         urlPath,
			Headers:     http.Header{"X-Asmux-Auth": {user.bridge.AS.Registration.AppToken}},
			RequestJSON: chats,
		})
	} else {
		existingChats := make(map[id.UserID][]id.RoomID)
		err = intent.GetAccountData(event.AccountDataDirectChats.Type, &existingChats)
		if err != nil {
			user.log.Warnln("Failed to get m.direct list to update it:", err)
			return
		}
		for userID, rooms := range existingChats {
			if _, ok := user.bridge.ParsePuppetMXID(userID); !ok {
				// This is not a ghost user, include it in the new list
				chats[userID] = rooms
			} else if _, ok := chats[userID]; !ok && method == http.MethodPatch {
				// This is a ghost user, but we're not replacing the whole list, so include it too
				chats[userID] = rooms
			}
		}
		err = intent.SetAccountData(event.AccountDataDirectChats.Type, &chats)
	}
	if err != nil {
		user.log.Warnln("Failed to update m.direct list:", err)
	}
}

func (user *User) HandleContactList(contacts []whatsapp.Contact) {
	contactMap := make(map[whatsapp.JID]whatsapp.Contact)
	for _, contact := range contacts {
		contactMap[contact.JID] = contact
	}
	go user.syncPuppets(contactMap)
}

func (user *User) syncPuppets(contacts map[whatsapp.JID]whatsapp.Contact) {
	if contacts == nil {
		contacts = user.Conn.Store.Contacts
	}

	_, hasSelf := contacts[user.JID]
	if !hasSelf {
		contacts[user.JID] = whatsapp.Contact{
			Name:   user.pushName,
			Notify: user.pushName,
			JID:    user.JID,
		}
	}

	user.log.Infoln("Syncing puppet info from contacts")
	for jid, contact := range contacts {
		if strings.HasSuffix(jid, whatsapp.NewUserSuffix) {
			puppet := user.bridge.GetPuppetByJID(contact.JID)
			puppet.Sync(user, contact)
		} else if strings.HasSuffix(jid, whatsapp.BroadcastSuffix) {
			portal := user.GetPortalByJID(contact.JID)
			portal.Sync(user, contact)
		}
	}
	user.log.Infoln("Finished syncing puppet info from contacts")
}

func (user *User) updateLastConnectionIfNecessary() {
	if user.LastConnection+60 < time.Now().Unix() {
		user.UpdateLastConnection()
	}
}

func (user *User) HandleError(err error) {
	if !errors.Is(err, whatsapp.ErrInvalidWsData) {
		user.log.Errorfln("WhatsApp error: %v", err)
	}
	if closed, ok := err.(*whatsapp.ErrConnectionClosed); ok {
		user.bridge.Metrics.TrackDisconnection(user.MXID)
		if closed.Code == 1000 && user.cleanDisconnection {
			user.cleanDisconnection = false
			if !user.bridge.Config.Bridge.AggressiveReconnect {
				user.sendBridgeState(BridgeState{Error: WANotConnected})
				user.bridge.Metrics.TrackConnectionState(user.JID, false)
				user.log.Infoln("Clean disconnection by server")
				return
			} else {
				user.log.Debugln("Clean disconnection by server, but aggressive reconnection is enabled")
			}
		}
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection was closed with websocket status code %d", closed.Code))
	} else if failed, ok := err.(*whatsapp.ErrConnectionFailed); ok {
		disconnectErr := user.Conn.Disconnect()
		if disconnectErr != nil {
			user.log.Warnln("Failed to disconnect after connection fail:", disconnectErr)
		}
		user.bridge.Metrics.TrackDisconnection(user.MXID)
		user.ConnectionErrors++
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection failed: %v", failed.Err))
	} else if err == whatsapp.ErrPingFalse || err == whatsapp.ErrWebsocketKeepaliveFailed {
		disconnectErr := user.Conn.Disconnect()
		if disconnectErr != nil {
			user.log.Warnln("Failed to disconnect after failed ping:", disconnectErr)
		}
		user.bridge.Metrics.TrackDisconnection(user.MXID)
		user.ConnectionErrors++
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection failed: %v", err))
	}
	// Otherwise unknown error, probably mostly harmless
}

func (user *User) tryReconnect(msg string) {
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	if user.ConnectionErrors > user.bridge.Config.Bridge.MaxConnectionAttempts {
		user.sendMarkdownBridgeAlert("%s. Use the `reconnect` command to reconnect.", msg)
		user.sendBridgeState(BridgeState{Error: WANotConnected})
		return
	}
	if user.bridge.Config.Bridge.ReportConnectionRetry {
		user.sendBridgeNotice("%s. Reconnecting...", msg)
		// Don't want the same error to be repeated
		msg = ""
	}
	var tries uint
	var exponentialBackoff bool
	baseDelay := time.Duration(user.bridge.Config.Bridge.ConnectionRetryDelay)
	if baseDelay < 0 {
		exponentialBackoff = true
		baseDelay = -baseDelay + 1
	}
	delay := baseDelay
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	user.cancelReconnect = cancel
	for user.ConnectionErrors <= user.bridge.Config.Bridge.MaxConnectionAttempts {
		select {
		case <-ctx.Done():
			user.log.Debugln("tryReconnect context cancelled, aborting reconnection attempts")
			return
		default:
		}
		user.sendBridgeState(BridgeState{Error: WAConnecting})
		err := user.Conn.Restore(true, ctx)
		if err == nil {
			user.ConnectionErrors = 0
			if user.bridge.Config.Bridge.ReportConnectionRetry {
				user.sendBridgeNotice("Reconnected successfully")
			}
			user.PostLogin()
			return
		} else if errors.Is(err, whatsapp.ErrBadRequest) {
			user.log.Warnln("Got init 400 error when trying to reconnect, resetting connection...")
			err = user.Conn.Disconnect()
			if err != nil {
				user.log.Debugln("Error while disconnecting for connection reset:", err)
			}
		} else if errors.Is(err, whatsapp.ErrUnpaired) {
			user.log.Errorln("Got init 401 (unpaired) error when trying to reconnect, not retrying")
			user.removeFromJIDMap()
			//user.JID = ""
			user.SetSession(nil)
			user.DeleteConnection()
			user.sendMarkdownBridgeAlert("\u26a0 Failed to reconnect to WhatsApp: unpaired from phone. " +
				"To re-pair your phone, log in again.")
			user.sendBridgeState(BridgeState{Error: WANotLoggedIn})
			return
		} else if errors.Is(err, whatsapp.ErrAlreadyLoggedIn) {
			user.log.Warnln("Reconnection said we're already logged in, not trying anymore")
			return
		} else {
			user.log.Errorln("Error while trying to reconnect after disconnection:", err)
		}
		tries++
		user.ConnectionErrors++
		if user.ConnectionErrors <= user.bridge.Config.Bridge.MaxConnectionAttempts {
			if exponentialBackoff {
				delay = (1 << tries) + baseDelay
			}
			if user.bridge.Config.Bridge.ReportConnectionRetry {
				user.sendBridgeNotice("Reconnection attempt failed: %v. Retrying in %d seconds...", err, delay)
			}
			time.Sleep(delay * time.Second)
		}
	}

	user.sendBridgeState(BridgeState{Error: WANotConnected})
	if user.bridge.Config.Bridge.ReportConnectionRetry {
		user.sendMarkdownBridgeAlert("%d reconnection attempts failed. Use the `reconnect` command to try to reconnect manually.", tries)
	} else {
		user.sendMarkdownBridgeAlert("\u26a0 %s. Additionally, %d reconnection attempts failed. Use the `reconnect` command to try to reconnect.", msg, tries)
	}
}

func (user *User) PortalKey(jid whatsapp.JID) database.PortalKey {
	return database.NewPortalKey(jid, user.JID)
}

func (user *User) GetPortalByJID(jid whatsapp.JID) *Portal {
	return user.bridge.GetPortalByJID(user.PortalKey(jid))
}

func (user *User) runMessageRingBuffer() {
	for msg := range user.messageInput {
		select {
		case user.messageOutput <- msg:
			user.bridge.Metrics.TrackBufferLength(user.MXID, len(user.messageOutput))
		default:
			dropped := <-user.messageOutput
			user.log.Warnln("Buffer is full, dropping message in", dropped.chat)
			user.messageOutput <- msg
		}
	}
}

func (user *User) handleMessageLoop() {
	for {
		select {
		case msg := <-user.messageOutput:
			user.bridge.Metrics.TrackBufferLength(user.MXID, len(user.messageOutput))
			user.GetPortalByJID(msg.chat).messages <- msg
		case <-user.syncStart:
			user.log.Debugln("Processing of incoming messages is locked")
			user.bridge.Metrics.TrackSyncLock(user.JID, true)
			user.syncWait.Wait()
			user.bridge.Metrics.TrackSyncLock(user.JID, false)
			user.log.Debugln("Processing of incoming messages unlocked")
		}
	}
}

func (user *User) HandleNewContact(contact whatsapp.Contact) {
	user.log.Debugfln("Contact message: %+v", contact)
	if strings.HasSuffix(contact.JID, whatsapp.OldUserSuffix) {
		contact.JID = strings.Replace(contact.JID, whatsapp.OldUserSuffix, whatsapp.NewUserSuffix, -1)
	}
	if strings.HasSuffix(contact.JID, whatsapp.NewUserSuffix) {
		puppet := user.bridge.GetPuppetByJID(contact.JID)
		puppet.UpdateName(user, contact)
	} else if strings.HasSuffix(contact.JID, whatsapp.BroadcastSuffix) {
		portal := user.GetPortalByJID(contact.JID)
		portal.UpdateName(contact.Name, "", nil, true)
	}
}

func (user *User) HandleBatteryMessage(battery whatsapp.BatteryMessage) {
	user.log.Debugfln("Battery message: %+v", battery)
	var notice string
	if !battery.Plugged && battery.Percentage < 15 && user.batteryWarningsSent < 1 {
		notice = fmt.Sprintf("Phone battery low (%d %% remaining)", battery.Percentage)
		user.batteryWarningsSent = 1
	} else if !battery.Plugged && battery.Percentage < 5 && user.batteryWarningsSent < 2 {
		notice = fmt.Sprintf("Phone battery very low (%d %% remaining)", battery.Percentage)
		user.batteryWarningsSent = 2
	} else if battery.Percentage > 15 || battery.Plugged {
		user.batteryWarningsSent = 0
	}
	if notice != "" {
		go user.sendBridgeNotice("%s", notice)
	}
}

type FakeMessage struct {
	Text  string
	ID    string
	Alert bool
}

func (user *User) HandleCallInfo(info whatsapp.CallInfo) {
	if info.Data != nil {
		return
	}
	data := FakeMessage{
		ID: info.ID,
	}
	switch info.Type {
	case whatsapp.CallOffer:
		if !user.bridge.Config.Bridge.CallNotices.Start {
			return
		}
		data.Text = "Incoming call"
		data.Alert = true
	case whatsapp.CallOfferVideo:
		if !user.bridge.Config.Bridge.CallNotices.Start {
			return
		}
		data.Text = "Incoming video call"
		data.Alert = true
	case whatsapp.CallTerminate:
		if !user.bridge.Config.Bridge.CallNotices.End {
			return
		}
		data.Text = "Call ended"
		data.ID += "E"
	default:
		return
	}
	portal := user.GetPortalByJID(info.From)
	if portal != nil {
		portal.messages <- PortalMessage{info.From, user, data, 0}
	}
}

func (user *User) HandlePresence(info whatsapp.PresenceEvent) {
	puppet := user.bridge.GetPuppetByJID(info.SenderJID)
	switch info.Status {
	case whatsapp.PresenceUnavailable:
		_ = puppet.DefaultIntent().SetPresence("offline")
	case whatsapp.PresenceAvailable:
		if len(puppet.typingIn) > 0 && puppet.typingAt+15 > time.Now().Unix() {
			portal := user.bridge.GetPortalByMXID(puppet.typingIn)
			_, _ = puppet.IntentFor(portal).UserTyping(puppet.typingIn, false, 0)
			puppet.typingIn = ""
			puppet.typingAt = 0
		} else {
			_ = puppet.DefaultIntent().SetPresence("online")
		}
	case whatsapp.PresenceComposing:
		portal := user.GetPortalByJID(info.JID)
		if len(puppet.typingIn) > 0 && puppet.typingAt+15 > time.Now().Unix() {
			if puppet.typingIn == portal.MXID {
				return
			}
			_, _ = puppet.IntentFor(portal).UserTyping(puppet.typingIn, false, 0)
		}
		puppet.typingIn = portal.MXID
		puppet.typingAt = time.Now().Unix()
		_, _ = puppet.IntentFor(portal).UserTyping(portal.MXID, true, 15*1000)
	}
}

func (user *User) HandleMsgInfo(info whatsapp.JSONMsgInfo) {
	if (info.Command == whatsapp.MsgInfoCommandAck || info.Command == whatsapp.MsgInfoCommandAcks) && info.Acknowledgement == whatsapp.AckMessageRead {
		portal := user.GetPortalByJID(info.ToJID)
		if len(portal.MXID) == 0 {
			return
		}

		intent := user.bridge.GetPuppetByJID(info.SenderJID).IntentFor(portal)
		for _, msgID := range info.IDs {
			msg := user.bridge.DB.Message.GetByJID(portal.Key, msgID)
			if msg == nil || msg.IsFakeMXID() {
				continue
			}

			err := intent.MarkReadWithContent(portal.MXID, msg.MXID, &CustomReadReceipt{DoublePuppet: intent.IsCustomPuppet})
			if err != nil {
				user.log.Warnfln("Failed to mark message %s as read by %s: %v", msg.MXID, info.SenderJID, err)
			}
		}
	}
}

func (user *User) HandleReceivedMessage(received whatsapp.ReceivedMessage) {
	if received.Type == "read" {
		go user.markSelfRead(received.Jid, received.Index)
	} else {
		user.log.Debugfln("Unknown received message type: %+v", received)
	}
}

func (user *User) HandleReadMessage(read whatsapp.ReadMessage) {
	user.log.Debugfln("Received chat read message: %+v", read)
	go user.markSelfRead(read.Jid, "")
}

func (user *User) markSelfRead(jid, messageID string) {
	if strings.HasSuffix(jid, whatsapp.OldUserSuffix) {
		jid = strings.Replace(jid, whatsapp.OldUserSuffix, whatsapp.NewUserSuffix, -1)
	}
	puppet := user.bridge.GetPuppetByJID(user.JID)
	if puppet == nil {
		return
	}
	intent := puppet.CustomIntent()
	if intent == nil {
		return
	}
	portal := user.GetPortalByJID(jid)
	if portal == nil {
		return
	}
	var message *database.Message
	if messageID == "" {
		message = user.bridge.DB.Message.GetLastInChat(portal.Key)
		if message == nil {
			return
		}
		user.log.Debugfln("User read chat %s/%s in WhatsApp mobile (last known event: %s/%s)", portal.Key.JID, portal.MXID, message.JID, message.MXID)
	} else {
		message = user.bridge.DB.Message.GetByJID(portal.Key, messageID)
		if message == nil || message.IsFakeMXID() {
			return
		}
		user.log.Debugfln("User read message %s/%s in %s/%s in WhatsApp mobile", message.JID, message.MXID, portal.Key.JID, portal.MXID)
	}
	err := intent.MarkReadWithContent(portal.MXID, message.MXID, &CustomReadReceipt{DoublePuppet: true})
	if err != nil {
		user.log.Warnfln("Failed to bridge own read receipt in %s: %v", jid, err)
	}
}

func (user *User) HandleCommand(cmd whatsapp.JSONCommand) {
	switch cmd.Type {
	case whatsapp.CommandPicture:
		if strings.HasSuffix(cmd.JID, whatsapp.NewUserSuffix) {
			puppet := user.bridge.GetPuppetByJID(cmd.JID)
			go puppet.UpdateAvatar(user, cmd.ProfilePicInfo)
		} else if user.bridge.Config.Bridge.ChatMetaSync {
			portal := user.GetPortalByJID(cmd.JID)
			go portal.UpdateAvatar(user, cmd.ProfilePicInfo, true)
		}
	case whatsapp.CommandDisconnect:
		if cmd.Kind == "replaced" {
			user.cleanDisconnection = true
			go user.sendMarkdownBridgeAlert("\u26a0 Your WhatsApp connection was closed by the server because you opened another WhatsApp Web client.\n\n" +
				"Use the `reconnect` command to disconnect the other client and resume bridging.")
		} else {
			user.log.Warnln("Unknown kind of disconnect:", string(cmd.Raw))
			go user.sendMarkdownBridgeAlert("\u26a0 Your WhatsApp connection was closed by the server (reason code: %s).\n\n"+
				"Use the `reconnect` command to reconnect.", cmd.Kind)
		}
	}
}

func (user *User) HandleChatUpdate(cmd whatsapp.ChatUpdate) {
	if cmd.Command != whatsapp.ChatUpdateCommandAction {
		return
	}

	portal := user.GetPortalByJID(cmd.JID)
	if len(portal.MXID) == 0 {
		if cmd.Data.Action == whatsapp.ChatActionIntroduce || cmd.Data.Action == whatsapp.ChatActionCreate {
			go func() {
				err := portal.CreateMatrixRoom(user)
				if err != nil {
					user.log.Errorln("Failed to create portal room after receiving join event:", err)
				}
			}()
		}
		return
	}

	// These don't come down the message history :(
	switch cmd.Data.Action {
	case whatsapp.ChatActionAddTopic:
		go portal.UpdateTopic(cmd.Data.AddTopic.Topic, cmd.Data.SenderJID, nil, true)
	case whatsapp.ChatActionRemoveTopic:
		go portal.UpdateTopic("", cmd.Data.SenderJID, nil, true)
	case whatsapp.ChatActionRemove:
		// We ignore leaving groups in the message history to avoid accidentally leaving rejoined groups,
		// but if we get a real-time command that says we left, it should be safe to bridge it.
		if !user.bridge.Config.Bridge.ChatMetaSync {
			for _, jid := range cmd.Data.UserChange.JIDs {
				if jid == user.JID {
					go portal.HandleWhatsAppKick(nil, cmd.Data.SenderJID, cmd.Data.UserChange.JIDs)
					break
				}
			}
		}
	}

	if !user.bridge.Config.Bridge.ChatMetaSync {
		// Ignore chat update commands, we're relying on the message history.
		return
	}

	switch cmd.Data.Action {
	case whatsapp.ChatActionNameChange:
		go portal.UpdateName(cmd.Data.NameChange.Name, cmd.Data.SenderJID, nil, true)
	case whatsapp.ChatActionPromote:
		go portal.ChangeAdminStatus(cmd.Data.UserChange.JIDs, true)
	case whatsapp.ChatActionDemote:
		go portal.ChangeAdminStatus(cmd.Data.UserChange.JIDs, false)
	case whatsapp.ChatActionAnnounce:
		go portal.RestrictMessageSending(cmd.Data.Announce)
	case whatsapp.ChatActionRestrict:
		go portal.RestrictMetadataChanges(cmd.Data.Restrict)
	case whatsapp.ChatActionRemove:
		go portal.HandleWhatsAppKick(nil, cmd.Data.SenderJID, cmd.Data.UserChange.JIDs)
	case whatsapp.ChatActionAdd:
		go portal.HandleWhatsAppInvite(user, cmd.Data.SenderJID, nil, cmd.Data.UserChange.JIDs)
	case whatsapp.ChatActionIntroduce:
		if cmd.Data.SenderJID != "unknown" {
			go portal.Sync(user, whatsapp.Contact{JID: portal.Key.JID})
		}
	}
}

func (user *User) HandleConnInfo(info whatsapp.ConnInfo) {
	if user.Session != nil && info.Connected && len(info.ClientToken) > 0 {
		user.log.Debugln("Received new tokens")
		user.Session.ClientToken = info.ClientToken
		user.Session.ServerToken = info.ServerToken
		user.Session.Wid = info.WID
		user.Update()
	}
	if len(info.PushName) > 0 {
		user.pushName = info.PushName
	}
}

func (user *User) HandleJSONMessage(evt whatsapp.RawJSONMessage) {
	if !json.Valid(evt.RawMessage) {
		return
	}
	user.log.Debugfln("JSON message with tag %s: %s", evt.Tag, evt.RawMessage)
	user.updateLastConnectionIfNecessary()
}

func (user *User) NeedsRelaybot(portal *Portal) bool {
	return !user.HasSession() || !user.IsInPortal(portal.Key)
}
