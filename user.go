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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	log "maunium.net/go/maulogger/v2"

	"github.com/Rhymen/go-whatsapp"
	waBinary "github.com/Rhymen/go-whatsapp/binary"
	waProto "github.com/Rhymen/go-whatsapp/binary/proto"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

type User struct {
	*database.User
	Conn *whatsappExt.ExtendedConn

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

	chatListReceived chan struct{}
	syncPortalsDone  chan struct{}

	messages chan PortalMessage

	syncStart chan struct{}
	syncWait  sync.WaitGroup

	mgmtCreateLock sync.Mutex
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

func (bridge *Bridge) GetUserByJID(userID types.WhatsAppID) *User {
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
		messages:         make(chan PortalMessage, bridge.Config.Bridge.UserMessageBuffer),
	}
	user.RelaybotWhitelisted = user.bridge.Config.Bridge.Permissions.IsRelaybotWhitelisted(user.MXID)
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.MXID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.MXID)
	go user.handleMessageLoop()
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
	user.Session = session
	if session == nil {
		user.LastConnection = 0
	}
	user.Update()
}

func (user *User) Connect(evenIfNoSession bool) bool {
	if user.Conn != nil {
		return true
	} else if !evenIfNoSession && user.Session == nil {
		return false
	}
	user.log.Debugln("Connecting to WhatsApp")
	timeout := time.Duration(user.bridge.Config.Bridge.ConnectionTimeout)
	if timeout == 0 {
		timeout = 20
	}
	conn, err := whatsapp.NewConn(timeout * time.Second)
	if err != nil {
		user.log.Errorln("Failed to connect to WhatsApp:", err)
		user.sendMarkdownBridgeAlert("\u26a0 Failed to connect to WhatsApp server. " +
			"This indicates a network problem on the bridge server. See bridge logs for more info.")
		return false
	}
	user.Conn = whatsappExt.ExtendConn(conn)
	_ = user.Conn.SetClientName(user.bridge.Config.WhatsApp.OSName, user.bridge.Config.WhatsApp.BrowserName, WAVersion)
	user.log.Debugln("WhatsApp connection successful")
	user.Conn.AddHandler(user)
	return user.RestoreSession()
}

func (user *User) RestoreSession() bool {
	if user.Session != nil {
		sess, err := user.Conn.RestoreWithSession(*user.Session)
		if err == whatsapp.ErrAlreadyLoggedIn {
			return true
		} else if err != nil {
			user.log.Errorln("Failed to restore session:", err)
			if errors.Is(err, whatsapp.ErrUnpaired) {
				user.sendMarkdownBridgeAlert("\u26a0 Failed to connect to WhatsApp: unpaired from phone. " +
					"To re-pair your phone, use `delete-session` and then `login`.")
			} else {
				user.sendMarkdownBridgeAlert("\u26a0 Failed to connect to WhatsApp. Make sure WhatsApp " +
					"on your phone is reachable and use `reconnect` to try connecting again.")
			}
			user.log.Debugln("Disconnecting due to failed session restore...")
			_, err := user.Conn.Disconnect()
			if err != nil {
				user.log.Errorln("Failed to disconnect after failed session restore:", err)
			}
			return false
		}
		user.ConnectionErrors = 0
		user.SetSession(&sess)
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
	session, err := user.Conn.LoginWithRetry(qrChan, user.bridge.Config.Bridge.LoginQRRegenCount)
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
	user.ConnectionErrors = 0
	user.JID = strings.Replace(user.Conn.Info.Wid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, 1)
	user.addToJIDMap()
	user.SetSession(&session)
	ce.Reply("Successfully logged in, synchronizing chats...")
	user.PostLogin()
}

type Chat struct {
	Portal          *Portal
	LastMessageTime uint64
	Contact         whatsapp.Contact
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
	user.bridge.Metrics.TrackConnectionState(user.JID, true)
	user.bridge.Metrics.TrackLoginState(user.JID, true)
	user.log.Debugln("Locking processing of incoming messages and starting post-login sync")
	user.syncWait.Add(1)
	user.syncStart <- struct{}{}
	go user.intPostLogin()
}

func (user *User) tryAutomaticDoublePuppeting() {
	if len(user.bridge.Config.Bridge.LoginSharedSecret) == 0 {
		// Automatic login not enabled
		return
	} else if _, homeserver, _ := user.MXID.Parse(); homeserver != user.bridge.Config.Homeserver.Domain {
		// user is on another homeserver
		return
	}

	puppet := user.bridge.GetPuppetByJID(user.JID)
	if len(puppet.CustomMXID) > 0 {
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
	err := user.Conn.AdminTest()
	if err != nil {
		user.log.Errorfln("Post-connection ping failed: %v. Disconnecting and then reconnecting after a second", err)
		sess, disconnectErr := user.Conn.Disconnect()
		if disconnectErr != nil {
			user.log.Warnln("Error while disconnecting after failed post-connection ping:", disconnectErr)
		} else {
			user.Session = &sess
		}
		user.bridge.Metrics.TrackDisconnection(user.MXID)
		go func() {
			time.Sleep(1 * time.Second)
			user.tryReconnect(fmt.Sprintf("Post-connection ping failed: %v", err))
		}()
		return false
	} else {
		user.log.Debugln("Post-connection ping OK")
		return true
	}
}

func (user *User) intPostLogin() {
	defer user.syncWait.Done()
	user.lastReconnection = time.Now().Unix()
	user.createCommunity()
	user.tryAutomaticDoublePuppeting()

	select {
	case <-user.chatListReceived:
		user.log.Debugln("Chat list receive confirmation received in PostLogin")
	case <-time.After(time.Duration(user.bridge.Config.Bridge.ChatListWait) * time.Second):
		user.log.Warnln("Timed out waiting for chat list to arrive!")
		user.postConnPing()
		return
	}
	if !user.postConnPing() {
		return
	}
	select {
	case <-user.syncPortalsDone:
		user.log.Debugln("Post-login portal sync complete, unlocking processing of incoming messages.")
	case <-time.After(time.Duration(user.bridge.Config.Bridge.PortalSyncWait) * time.Second):
		user.log.Warnln("Timed out waiting for portal sync to complete! Unlocking processing of incoming messages.")
	}
}

func (user *User) HandleStreamEvent(evt whatsappExt.StreamEvent) {
	if evt.Type == whatsappExt.StreamSleep {
		if user.lastReconnection+60 > time.Now().Unix() {
			user.lastReconnection = 0
			user.log.Infoln("Stream went to sleep soon after reconnection, making new post-connection ping in 20 seconds")
			go func() {
				time.Sleep(20 * time.Second)
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
		chatMap[chat.Jid] = chat
	}
	for _, chat := range chats {
		chatMap[chat.Jid] = chat
	}
	select {
	case user.chatListReceived <- struct{}{}:
	default:
	}
	go user.syncPortals(chatMap, false)
}

func (user *User) syncPortals(chatMap map[string]whatsapp.Chat, createAll bool) {
	if chatMap == nil {
		chatMap = user.Conn.Store.Chats
	}
	user.log.Infoln("Reading chat list")
	chats := make(ChatList, 0, len(chatMap))
	existingKeys := user.GetInCommunityMap()
	portalKeys := make([]database.PortalKeyWithMeta, 0, len(chatMap))
	for _, chat := range chatMap {
		ts, err := strconv.ParseUint(chat.LastMessageTime, 10, 64)
		if err != nil {
			user.log.Warnfln("Non-integer last message time in %s: %s", chat.Jid, chat.LastMessageTime)
			continue
		}
		portal := user.GetPortalByJID(chat.Jid)

		chats = append(chats, Chat{
			Portal:          portal,
			Contact:         user.Conn.Store.Contacts[chat.Jid],
			LastMessageTime: ts,
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
	limit := user.bridge.Config.Bridge.InitialChatSync
	if limit < 0 {
		limit = len(chats)
	}
	now := uint64(time.Now().Unix())
	user.log.Infoln("Syncing portals")
	for i, chat := range chats {
		if chat.LastMessageTime+user.bridge.Config.Bridge.SyncChatMaxAge < now {
			break
		}
		create := (chat.LastMessageTime >= user.LastConnection && user.LastConnection > 0) || i < limit
		if len(chat.Portal.MXID) > 0 || create || createAll {
			chat.Portal.Sync(user, chat.Contact)
			err := chat.Portal.BackfillHistory(user, chat.LastMessageTime)
			if err != nil {
				chat.Portal.log.Errorln("Error backfilling history:", err)
			}
		}
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
		urlPath := intent.BuildBaseURL("_matrix", "client", "unstable", "net.maunium.asmux", "dms")
		_, err = intent.MakeFullRequest(method, urlPath, http.Header{
			"X-Asmux-Auth": {user.bridge.AS.Registration.AppToken},
		}, chats, nil)
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
	contactMap := make(map[string]whatsapp.Contact)
	for _, contact := range contacts {
		contactMap[contact.Jid] = contact
	}
	go user.syncPuppets(contactMap)
}

func (user *User) syncPuppets(contacts map[string]whatsapp.Contact) {
	if contacts == nil {
		contacts = user.Conn.Store.Contacts
	}
	user.log.Infoln("Syncing puppet info from contacts")
	for jid, contact := range contacts {
		if strings.HasSuffix(jid, whatsappExt.NewUserSuffix) {
			puppet := user.bridge.GetPuppetByJID(contact.Jid)
			puppet.Sync(user, contact)
		}
	}
	user.log.Infoln("Finished syncing puppet info from contacts")
}

func (user *User) updateLastConnectionIfNecessary() {
	if user.LastConnection+60 < uint64(time.Now().Unix()) {
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
			user.bridge.Metrics.TrackConnectionState(user.JID, false)
			user.cleanDisconnection = false
			user.log.Infoln("Clean disconnection by server")
			return
		}
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection was closed with websocket status code %d", closed.Code))
	} else if failed, ok := err.(*whatsapp.ErrConnectionFailed); ok {
		user.bridge.Metrics.TrackDisconnection(user.MXID)
		user.ConnectionErrors++
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection failed: %v", failed.Err))
	}
	// Otherwise unknown error, probably mostly harmless
}

func (user *User) tryReconnect(msg string) {
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	if user.ConnectionErrors > user.bridge.Config.Bridge.MaxConnectionAttempts {
		user.sendMarkdownBridgeAlert("%s. Use the `reconnect` command to reconnect.", msg)
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
	for user.ConnectionErrors <= user.bridge.Config.Bridge.MaxConnectionAttempts {
		err := user.Conn.Restore()
		if err == nil {
			user.ConnectionErrors = 0
			if user.bridge.Config.Bridge.ReportConnectionRetry {
				user.sendBridgeNotice("Reconnected successfully")
			}
			user.PostLogin()
			return
		} else if err.Error() == "init responded with 400" {
			user.log.Infoln("Got init 400 error when trying to reconnect, resetting connection...")
			sess, err := user.Conn.Disconnect()
			if err != nil {
				user.log.Debugln("Error while disconnecting for connection reset:", err)
			}
			if len(sess.Wid) > 0 {
				user.SetSession(&sess)
			}
		}
		user.log.Errorln("Error while trying to reconnect after disconnection:", err)
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

	if user.bridge.Config.Bridge.ReportConnectionRetry {
		user.sendMarkdownBridgeAlert("%d reconnection attempts failed. Use the `reconnect` command to try to reconnect manually.", tries)
	} else {
		user.sendMarkdownBridgeAlert("\u26a0 %s. Additionally, %d reconnection attempts failed. Use the `reconnect` command to try to reconnect.", msg, tries)
	}
}

func (user *User) ShouldCallSynchronously() bool {
	return true
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

func (user *User) handleMessageLoop() {
	for {
		select {
		case msg := <-user.messages:
			user.GetPortalByJID(msg.chat).messages <- msg
		case <-user.syncStart:
			user.syncWait.Wait()
		}
	}
}

func (user *User) putMessage(message PortalMessage) {
	select {
	case user.messages <- message:
	default:
		user.log.Warnln("Buffer is full, dropping message in", message.chat)
	}
}

func (user *User) HandleNewContact(contact whatsapp.Contact) {
	user.log.Debugfln("Contact message: %+v", contact)
	go func() {
		if strings.HasSuffix(contact.Jid, whatsappExt.OldUserSuffix) {
			contact.Jid = strings.Replace(contact.Jid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, -1)
		}
		puppet := user.bridge.GetPuppetByJID(contact.Jid)
		puppet.UpdateName(user, contact)
	}()
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

func (user *User) HandleTextMessage(message whatsapp.TextMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleImageMessage(message whatsapp.ImageMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleStickerMessage(message whatsapp.StickerMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleVideoMessage(message whatsapp.VideoMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleAudioMessage(message whatsapp.AudioMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleDocumentMessage(message whatsapp.DocumentMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleContactMessage(message whatsapp.ContactMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleLocationMessage(message whatsapp.LocationMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleMessageRevoke(message whatsappExt.MessageRevocation) {
	user.putMessage(PortalMessage{message.RemoteJid, user, message, 0})
}

type FakeMessage struct {
	Text  string
	ID    string
	Alert bool
}

func (user *User) HandleCallInfo(info whatsappExt.CallInfo) {
	if info.Data != nil {
		return
	}
	data := FakeMessage{
		ID: info.ID,
	}
	switch info.Type {
	case whatsappExt.CallOffer:
		if !user.bridge.Config.Bridge.CallNotices.Start {
			return
		}
		data.Text = "Incoming call"
		data.Alert = true
	case whatsappExt.CallOfferVideo:
		if !user.bridge.Config.Bridge.CallNotices.Start {
			return
		}
		data.Text = "Incoming video call"
		data.Alert = true
	case whatsappExt.CallTerminate:
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

func (user *User) HandlePresence(info whatsappExt.Presence) {
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

func (user *User) HandleMsgInfo(info whatsappExt.MsgInfo) {
	if (info.Command == whatsappExt.MsgInfoCommandAck || info.Command == whatsappExt.MsgInfoCommandAcks) && info.Acknowledgement == whatsappExt.AckMessageRead {
		portal := user.GetPortalByJID(info.ToJID)
		if len(portal.MXID) == 0 {
			return
		}

		go func() {
			intent := user.bridge.GetPuppetByJID(info.SenderJID).IntentFor(portal)
			for _, msgID := range info.IDs {
				msg := user.bridge.DB.Message.GetByJID(portal.Key, msgID)
				if msg == nil {
					continue
				}

				err := intent.MarkRead(portal.MXID, msg.MXID)
				if err != nil {
					user.log.Warnln("Failed to mark message %s as read by %s: %v", msg.MXID, info.SenderJID, err)
				}
			}
		}()
	}
}

func (user *User) HandleReceivedMessage(received whatsapp.ReceivedMessage) {
	if received.Type == "read" {
		user.markSelfRead(received.Jid, received.Index)
	} else {
		user.log.Debugfln("Unknown received message type: %+v", received)
	}
}

func (user *User) HandleReadMessage(read whatsapp.ReadMessage) {
	user.log.Debugfln("Received chat read message: %+v", read)
	user.markSelfRead(read.Jid, "")
}

func (user *User) markSelfRead(jid, messageID string) {
	if strings.HasSuffix(jid, whatsappExt.OldUserSuffix) {
		jid = strings.Replace(jid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, -1)
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
		if message == nil {
			return
		}
		user.log.Debugfln("User read message %s/%s in %s/%s in WhatsApp mobile", message.JID, message.MXID, portal.Key.JID, portal.MXID)
	}
	err := intent.MarkRead(portal.MXID, message.MXID)
	if err != nil {
		user.log.Warnfln("Failed to bridge own read receipt in %s: %v", jid, err)
	}
}

func (user *User) HandleCommand(cmd whatsappExt.Command) {
	switch cmd.Type {
	case whatsappExt.CommandPicture:
		if strings.HasSuffix(cmd.JID, whatsappExt.NewUserSuffix) {
			puppet := user.bridge.GetPuppetByJID(cmd.JID)
			go puppet.UpdateAvatar(user, cmd.ProfilePicInfo)
		} else {
			portal := user.GetPortalByJID(cmd.JID)
			go portal.UpdateAvatar(user, cmd.ProfilePicInfo, true)
		}
	case whatsappExt.CommandDisconnect:
		user.cleanDisconnection = true
		if cmd.Kind == "replaced" {
			go user.sendMarkdownBridgeAlert("\u26a0 Your WhatsApp connection was closed by the server because you opened another WhatsApp Web client.\n\n" +
				"Use the `reconnect` command to disconnect the other client and resume bridging.")
		} else {
			user.log.Warnln("Unknown kind of disconnect:", string(cmd.Raw))
			go user.sendMarkdownBridgeAlert("\u26a0 Your WhatsApp connection was closed by the server (reason code: %s).\n\n"+
				"Use the `reconnect` command to reconnect.", cmd.Kind)
		}
	}
}

func (user *User) HandleChatUpdate(cmd whatsappExt.ChatUpdate) {
	if cmd.Command != whatsappExt.ChatUpdateCommandAction {
		return
	}

	portal := user.GetPortalByJID(cmd.JID)
	if len(portal.MXID) == 0 {
		if cmd.Data.Action == whatsappExt.ChatActionIntroduce || cmd.Data.Action == whatsappExt.ChatActionCreate {
			go func() {
				err := portal.CreateMatrixRoom(user)
				if err != nil {
					user.log.Errorln("Failed to create portal room after receiving join event:", err)
				}
			}()
		}
		return
	}

	switch cmd.Data.Action {
	case whatsappExt.ChatActionNameChange:
		go portal.UpdateName(cmd.Data.NameChange.Name, cmd.Data.SenderJID, true)
	case whatsappExt.ChatActionAddTopic:
		go portal.UpdateTopic(cmd.Data.AddTopic.Topic, cmd.Data.SenderJID, true)
	case whatsappExt.ChatActionRemoveTopic:
		go portal.UpdateTopic("", cmd.Data.SenderJID, true)
	case whatsappExt.ChatActionPromote:
		go portal.ChangeAdminStatus(cmd.Data.UserChange.JIDs, true)
	case whatsappExt.ChatActionDemote:
		go portal.ChangeAdminStatus(cmd.Data.UserChange.JIDs, false)
	case whatsappExt.ChatActionAnnounce:
		go portal.RestrictMessageSending(cmd.Data.Announce)
	case whatsappExt.ChatActionRestrict:
		go portal.RestrictMetadataChanges(cmd.Data.Restrict)
	case whatsappExt.ChatActionRemove:
		go portal.HandleWhatsAppKick(cmd.Data.SenderJID, cmd.Data.UserChange.JIDs)
	case whatsappExt.ChatActionAdd:
		go portal.HandleWhatsAppInvite(cmd.Data.SenderJID, cmd.Data.UserChange.JIDs)
	case whatsappExt.ChatActionIntroduce:
		if cmd.Data.SenderJID != "unknown" {
			go portal.Sync(user, whatsapp.Contact{Jid: portal.Key.JID})
		}
	}
}

func (user *User) HandleJsonMessage(message string) {
	var msg json.RawMessage
	err := json.Unmarshal([]byte(message), &msg)
	if err != nil {
		return
	}
	user.log.Debugln("JSON message:", message)
	user.updateLastConnectionIfNecessary()
}

func (user *User) HandleRawMessage(message *waProto.WebMessageInfo) {
	user.updateLastConnectionIfNecessary()
}

func (user *User) HandleUnknownBinaryNode(node *waBinary.Node) {
	user.log.Debugfln("Unknown binary message: %+v", node)
}

func (user *User) NeedsRelaybot(portal *Portal) bool {
	return !user.HasSession() || !user.IsInPortal(portal.Key)
}
