// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/pushrules"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

type User struct {
	*database.User
	Client  *whatsmeow.Client
	Session *store.Device

	bridge *Bridge
	log    log.Logger

	Admin               bool
	Whitelisted         bool
	RelaybotWhitelisted bool

	IsRelaybot bool

	mgmtCreateLock sync.Mutex
	connLock       sync.Mutex

	qrListener    chan<- *events.QR
	loginListener chan<- *events.PairSuccess

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

func (bridge *Bridge) GetUserByJID(jid types.JID) *User {
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByUsername[jid.User]
	if !ok {
		return bridge.loadDBUser(bridge.DB.User.GetByUsername(jid.User), nil)
	}
	return user
}

func (user *User) addToJIDMap() {
	user.bridge.usersLock.Lock()
	user.bridge.usersByUsername[user.JID.User] = user
	user.bridge.usersLock.Unlock()
}

func (user *User) removeFromJIDMap(state BridgeStateEvent) {
	user.bridge.usersLock.Lock()
	jidUser, ok := user.bridge.usersByUsername[user.JID.User]
	if ok && user == jidUser {
		delete(user.bridge.usersByUsername, user.JID.User)
	}
	user.bridge.usersLock.Unlock()
	user.bridge.Metrics.TrackLoginState(user.JID, false)
	user.sendBridgeState(BridgeState{StateEvent: state, Error: WANotLoggedIn})
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
	if !user.JID.IsEmpty() {
		var err error
		user.Session, err = bridge.WAContainer.GetDevice(user.JID)
		if err != nil {
			user.log.Errorfln("Failed to scan user's whatsapp session: %v", err)
		} else if user.Session == nil {
			user.log.Warnfln("Didn't find session data for %s, treating user as logged out", user.JID)
			user.JID = types.EmptyJID
			user.Update()
		} else {
			bridge.usersByUsername[user.JID.User] = user
		}
	}
	if len(user.ManagementRoom) > 0 {
		bridge.managementRooms[user.ManagementRoom] = user
	}
	return user
}

func (user *User) GetPortals() []*Portal {
	// FIXME
	//keys := user.User.GetPortalKeys()
	//portals := make([]*Portal, len(keys))
	//
	//user.bridge.portalsLock.Lock()
	//for i, key := range keys {
	//	portal, ok := user.bridge.portalsByJID[key]
	//	if !ok {
	//		portal = user.bridge.loadDBPortal(user.bridge.DB.Portal.GetByJID(key), &key)
	//	}
	//	portals[i] = portal
	//}
	//user.bridge.portalsLock.Unlock()
	//return portals
	return nil
}

func (bridge *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: bridge,
		log:    bridge.Log.Sub("User").Sub(string(dbUser.MXID)),

		IsRelaybot: false,
	}
	user.RelaybotWhitelisted = user.bridge.Config.Bridge.Permissions.IsRelaybotWhitelisted(user.MXID)
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.MXID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.MXID)
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

type waLogger struct{ l log.Logger }

func (w *waLogger) Debugf(msg string, args ...interface{}) { w.l.Debugfln(msg, args...) }
func (w *waLogger) Infof(msg string, args ...interface{})  { w.l.Infofln(msg, args...) }
func (w *waLogger) Warnf(msg string, args ...interface{})  { w.l.Warnfln(msg, args...) }
func (w *waLogger) Errorf(msg string, args ...interface{}) { w.l.Errorfln(msg, args...) }
func (w *waLogger) Sub(module string) waLog.Logger         { return &waLogger{l: w.l.Sub(module)} }

func (user *User) Connect(evenIfNoSession bool) bool {
	user.connLock.Lock()
	defer user.connLock.Unlock()
	if user.Client != nil {
		return user.Client.IsConnected()
	} else if !evenIfNoSession && user.Session == nil {
		return false
	}
	user.log.Debugln("Connecting to WhatsApp")
	if user.Session != nil {
		user.sendBridgeState(BridgeState{StateEvent: StateConnecting, Error: WAConnecting})
	}
	if user.Session == nil {
		newSession := user.bridge.WAContainer.NewDevice()
		newSession.Log = &waLogger{user.log.Sub("Session")}
		user.Client = whatsmeow.NewClient(newSession, &waLogger{user.log.Sub("Client")})
	} else {
		user.Client = whatsmeow.NewClient(user.Session, &waLogger{user.log.Sub("Client")})
	}
	user.Client.AddEventHandler(user.HandleEvent)
	err := user.Client.Connect()
	if err != nil {
		user.log.Warnln("Error connecting to WhatsApp:", err)
		return false
	}
	return true
}

func (user *User) DeleteConnection() {
	user.connLock.Lock()
	defer user.connLock.Unlock()
	if user.Client == nil {
		return
	}
	user.Client.Disconnect()
	user.Client.RemoveEventHandlers()
	user.Client = nil
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
	user.sendBridgeState(BridgeState{StateEvent: StateBadCredentials, Error: WANotConnected})
}

func (user *User) HasSession() bool {
	return user.Session != nil
}

func (user *User) DeleteSession() {
	if user.Session != nil {
		err := user.Session.Delete()
		if err != nil {
			user.log.Warnln("Failed to delete session:", err)
		}
		user.Session = nil
	}
	if !user.JID.IsEmpty() {
		user.JID = types.EmptyJID
		user.Update()
	}
}

func (user *User) IsLoggedIn() bool {
	return user.Client != nil && user.Client.IsConnected() && user.Client.IsLoggedIn
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

func (user *User) HandleEvent(event interface{}) {
	switch v := event.(type) {
	case *events.LoggedOut:
		go user.handleLoggedOut()
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
		user.bridge.Metrics.TrackLoginState(user.JID, false)
	case *events.Connected:
		go user.sendBridgeState(BridgeState{StateEvent: StateConnected})
		user.bridge.Metrics.TrackConnectionState(user.JID, true)
		user.bridge.Metrics.TrackLoginState(user.JID, true)
		go func() {
			err := user.Client.SendPresence(types.PresenceUnavailable)
			if err != nil {
				user.log.Warnln("Failed to send initial presence:", err)
			}
		}()
		go user.tryAutomaticDoublePuppeting()
	case *events.PairSuccess:
		user.JID = v.ID
		user.addToJIDMap()
		user.Update()
		user.Session = user.Client.Store
		if user.loginListener != nil {
			select {
			case user.loginListener <- v:
				return
			default:
			}
		}
		user.log.Warnln("Got pair success event, but nothing waiting for it")
	case *events.QR:
		if user.qrListener != nil {
			select {
			case user.qrListener <- v:
				return
			default:
			}
		}
		user.log.Warnln("Got QR code event, but nothing waiting for it")
	case *events.ConnectFailure, *events.StreamError:
		go user.sendBridgeState(BridgeState{StateEvent: StateUnknownError})
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.Disconnected:
		go user.sendBridgeState(BridgeState{StateEvent: StateTransientDisconnect})
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.Contact:
		go user.syncPuppet(v.JID)
	case *events.PushName:
		go user.syncPuppet(v.JID)
	case *events.Receipt:
		go user.handleReceipt(v)
	case *events.Message:
		portal := user.GetPortalByJID(v.Info.Chat)
		portal.messages <- PortalMessage{v, user}
	case *events.Mute:
		portal := user.bridge.GetPortalByJID(user.PortalKey(v.JID))
		if portal != nil {
			var mutedUntil time.Time
			if v.Action.GetMuted() {
				mutedUntil = time.Unix(v.Action.GetMuteEndTimestamp(), 0)
			}
			go user.updateChatMute(nil, portal, mutedUntil)
		}
	case *events.Archive:
		portal := user.bridge.GetPortalByJID(user.PortalKey(v.JID))
		if portal != nil {
			go user.updateChatTag(nil, portal, user.bridge.Config.Bridge.ArchiveTag, v.Action.GetArchived())
		}
	case *events.Pin:
		portal := user.bridge.GetPortalByJID(user.PortalKey(v.JID))
		if portal != nil {
			go user.updateChatTag(nil, portal, user.bridge.Config.Bridge.PinnedTag, v.Action.GetPinned())
		}
	default:
		user.log.Debugfln("Unknown type of event in HandleEvent: %T", v)
	}
}

func (user *User) updateChatMute(intent *appservice.IntentAPI, portal *Portal, mutedUntil time.Time) {
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
	if mutedUntil.IsZero() && mutedUntil.Before(time.Now()) {
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

func (user *User) syncChatDoublePuppetDetails(doublePuppet *Puppet, portal *Portal, justCreated bool) {
	if doublePuppet == nil || doublePuppet.CustomIntent() == nil || len(portal.MXID) == 0 {
		return
	}
	intent := doublePuppet.CustomIntent()
	// FIXME this might not be possible to do anymore
	//if chat.UnreadCount == 0 && (justCreated || !user.bridge.Config.Bridge.MarkReadOnlyOnCreate) {
	//	lastMessage := user.bridge.DB.Message.GetLastInChatBefore(chat.Portal.Key, chat.ReceivedAt.Unix())
	//	if lastMessage != nil {
	//		err := intent.MarkReadWithContent(chat.Portal.MXID, lastMessage.MXID, &CustomReadReceipt{DoublePuppet: true})
	//		if err != nil {
	//			user.log.Warnfln("Failed to mark %s in %s as read after backfill: %v", lastMessage.MXID, chat.Portal.MXID, err)
	//		}
	//	}
	//} else if chat.UnreadCount == -1 {
	//	user.log.Debugfln("Invalid unread count (missing field?) in chat info %+v", chat.Source)
	//}
	if justCreated || !user.bridge.Config.Bridge.TagOnlyOnCreate {
		chat, err := user.Client.Store.ChatSettings.GetChatSettings(portal.Key.JID)
		if err != nil {
			user.log.Warnfln("Failed to get settings of %s: %v", portal.Key.JID, err)
			return
		}
		user.updateChatMute(intent, portal, chat.MutedUntil)
		user.updateChatTag(intent, portal, user.bridge.Config.Bridge.ArchiveTag, chat.Archived)
		user.updateChatTag(intent, portal, user.bridge.Config.Bridge.PinnedTag, chat.Pinned)
	}
}

//func (user *User) syncPortal(chat Chat) {
//	// Don't sync unless chat meta sync is enabled or portal doesn't exist
//	if user.bridge.Config.Bridge.ChatMetaSync || len(chat.Portal.MXID) == 0 {
//		failedToCreate := chat.Portal.Sync(user, chat.Contact)
//		if failedToCreate {
//			return
//		}
//	}
//	err := chat.Portal.BackfillHistory(user, chat.LastMessageTime)
//	if err != nil {
//		chat.Portal.log.Errorln("Error backfilling history:", err)
//	}
//}
//
//func (user *User) syncPortals(chatMap map[string]whatsapp.Chat, createAll bool) {
//	// TODO use contexts instead of checking if user.Conn is the same?
//	connAtStart := user.Conn
//
//	chats := user.collectChatList(chatMap)
//
//	limit := user.bridge.Config.Bridge.InitialChatSync
//	if limit < 0 {
//		limit = len(chats)
//	}
//	if user.Conn != connAtStart {
//		user.log.Debugln("Connection seems to have changed before sync, cancelling")
//		return
//	}
//	now := time.Now().Unix()
//	user.log.Infoln("Syncing portals")
//	doublePuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
//	for i, chat := range chats {
//		if chat.LastMessageTime+user.bridge.Config.Bridge.SyncChatMaxAge < now {
//			break
//		}
//		create := (chat.LastMessageTime >= user.LastConnection && user.LastConnection > 0) || i < limit
//		if len(chat.Portal.MXID) > 0 || create || createAll {
//			user.log.Debugfln("Syncing chat %+v", chat.Chat.Source)
//			justCreated := len(chat.Portal.MXID) == 0
//			user.syncPortal(chat)
//			user.syncChatDoublePuppetDetails(doublePuppet, chat, justCreated)
//		}
//	}
//	if user.Conn != connAtStart {
//		user.log.Debugln("Connection seems to have changed during sync, cancelling")
//		return
//	}
//	user.UpdateDirectChats(nil)
//
//	user.log.Infoln("Finished syncing portals")
//	select {
//	case user.syncPortalsDone <- struct{}{}:
//	default:
//	}
//}

func (user *User) getDirectChats() map[id.UserID][]id.RoomID {
	res := make(map[id.UserID][]id.RoomID)
	privateChats := user.bridge.DB.Portal.FindPrivateChats(user.JID.ToNonAD())
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

func (user *User) handleLoggedOut() {
	user.JID = types.EmptyJID
	user.Update()
	user.sendMarkdownBridgeAlert("Connecting to WhatsApp failed as the device was logged out. Please link the bridge to your phone again.")
	user.sendBridgeState(BridgeState{StateEvent: StateBadCredentials, Error: WANotLoggedIn})
}

func (user *User) PortalKey(jid types.JID) database.PortalKey {
	return database.NewPortalKey(jid, user.JID)
}

func (user *User) GetPortalByJID(jid types.JID) *Portal {
	return user.bridge.GetPortalByJID(user.PortalKey(jid))
}

func (user *User) syncPuppet(jid types.JID) {
	user.bridge.GetPuppetByJID(jid).SyncContact(user, false)
}

//func (user *User) HandlePresence(info whatsapp.PresenceEvent) {
//	puppet := user.bridge.GetPuppetByJID(info.SenderJID)
//	switch info.Status {
//	case whatsapp.PresenceUnavailable:
//		_ = puppet.DefaultIntent().SetPresence("offline")
//	case whatsapp.PresenceAvailable:
//		if len(puppet.typingIn) > 0 && puppet.typingAt+15 > time.Now().Unix() {
//			portal := user.bridge.GetPortalByMXID(puppet.typingIn)
//			_, _ = puppet.IntentFor(portal).UserTyping(puppet.typingIn, false, 0)
//			puppet.typingIn = ""
//			puppet.typingAt = 0
//		} else {
//			_ = puppet.DefaultIntent().SetPresence("online")
//		}
//	case whatsapp.PresenceComposing:
//		portal := user.GetPortalByJID(info.JID)
//		if len(puppet.typingIn) > 0 && puppet.typingAt+15 > time.Now().Unix() {
//			if puppet.typingIn == portal.MXID {
//				return
//			}
//			_, _ = puppet.IntentFor(portal).UserTyping(puppet.typingIn, false, 0)
//		}
//		if len(portal.MXID) > 0 {
//			puppet.typingIn = portal.MXID
//			puppet.typingAt = time.Now().Unix()
//			_, _ = puppet.IntentFor(portal).UserTyping(portal.MXID, true, 15*1000)
//		}
//	}
//}

func (user *User) handleReceipt(receipt *events.Receipt) {
	if receipt.Type != events.ReceiptTypeRead {
		return
	}
	portal := user.GetPortalByJID(receipt.Chat)
	if portal == nil || len(portal.MXID) == 0 {
		return
	}
	if receipt.IsFromMe {
		user.markSelfRead(portal, receipt.MessageID)
	} else {
		intent := user.bridge.GetPuppetByJID(receipt.Sender).IntentFor(portal)
		ok := user.markOtherRead(portal, intent, receipt.MessageID)
		if !ok {
			// Message not found, try any previous IDs
			for i := len(receipt.PreviousIDs) - 1; i >= 0; i-- {
				ok = user.markOtherRead(portal, intent, receipt.PreviousIDs[i])
				if ok {
					break
				}
			}
		}
	}
}

func (user *User) markOtherRead(portal *Portal, intent *appservice.IntentAPI, messageID types.MessageID) bool {
	msg := user.bridge.DB.Message.GetByJID(portal.Key, messageID)
	if msg == nil || msg.IsFakeMXID() {
		return false
	}

	err := intent.MarkReadWithContent(portal.MXID, msg.MXID, &CustomReadReceipt{DoublePuppet: intent.IsCustomPuppet})
	if err != nil {
		user.log.Warnfln("Failed to mark message %s as read by %s: %v", msg.MXID, intent.UserID, err)
	}
	return true
}

func (user *User) markSelfRead(portal *Portal, messageID types.MessageID) {
	puppet := user.bridge.GetPuppetByJID(user.JID)
	if puppet == nil {
		return
	}
	intent := puppet.CustomIntent()
	if intent == nil {
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
		user.log.Warnfln("Failed to bridge own read receipt in %s: %v", portal.Key.JID, err)
	}
}

//func (user *User) HandleCommand(cmd whatsapp.JSONCommand) {
//	switch cmd.Type {
//	case whatsapp.CommandPicture:
//		if strings.HasSuffix(cmd.JID, whatsapp.NewUserSuffix) {
//			puppet := user.bridge.GetPuppetByJID(cmd.JID)
//			go puppet.UpdateAvatar(user, cmd.ProfilePicInfo)
//		} else if user.bridge.Config.Bridge.ChatMetaSync {
//			portal := user.GetPortalByJID(cmd.JID)
//			go portal.UpdateAvatar(user, cmd.ProfilePicInfo, true)
//		}
//	case whatsapp.CommandDisconnect:
//		if cmd.Kind == "replaced" {
//			user.cleanDisconnection = true
//			go user.sendMarkdownBridgeAlert("\u26a0 Your WhatsApp connection was closed by the server because you opened another WhatsApp Web client.\n\n" +
//				"Use the `reconnect` command to disconnect the other client and resume bridging.")
//		} else {
//			user.log.Warnln("Unknown kind of disconnect:", string(cmd.Raw))
//			go user.sendMarkdownBridgeAlert("\u26a0 Your WhatsApp connection was closed by the server (reason code: %s).\n\n"+
//				"Use the `reconnect` command to reconnect.", cmd.Kind)
//		}
//	}
//}

//func (user *User) HandleChatUpdate(cmd whatsapp.ChatUpdate) {
//	if cmd.Command != whatsapp.ChatUpdateCommandAction {
//		return
//	}
//
//	portal := user.GetPortalByJID(cmd.JID)
//	if len(portal.MXID) == 0 {
//		if cmd.Data.Action == whatsapp.ChatActionIntroduce || cmd.Data.Action == whatsapp.ChatActionCreate {
//			go func() {
//				err := portal.CreateMatrixRoom(user)
//				if err != nil {
//					user.log.Errorln("Failed to create portal room after receiving join event:", err)
//				}
//			}()
//		}
//		return
//	}
//
//	// These don't come down the message history :(
//	switch cmd.Data.Action {
//	case whatsapp.ChatActionAddTopic:
//		go portal.UpdateTopic(cmd.Data.AddTopic.Topic, cmd.Data.SenderJID, nil, true)
//	case whatsapp.ChatActionRemoveTopic:
//		go portal.UpdateTopic("", cmd.Data.SenderJID, nil, true)
//	case whatsapp.ChatActionRemove:
//		// We ignore leaving groups in the message history to avoid accidentally leaving rejoined groups,
//		// but if we get a real-time command that says we left, it should be safe to bridge it.
//		if !user.bridge.Config.Bridge.ChatMetaSync {
//			for _, jid := range cmd.Data.UserChange.JIDs {
//				if jid == user.JID {
//					go portal.HandleWhatsAppKick(nil, cmd.Data.SenderJID, cmd.Data.UserChange.JIDs)
//					break
//				}
//			}
//		}
//	}
//
//	if !user.bridge.Config.Bridge.ChatMetaSync {
//		// Ignore chat update commands, we're relying on the message history.
//		return
//	}
//
//	switch cmd.Data.Action {
//	case whatsapp.ChatActionNameChange:
//		go portal.UpdateName(cmd.Data.NameChange.Name, cmd.Data.SenderJID, nil, true)
//	case whatsapp.ChatActionPromote:
//		go portal.ChangeAdminStatus(cmd.Data.UserChange.JIDs, true)
//	case whatsapp.ChatActionDemote:
//		go portal.ChangeAdminStatus(cmd.Data.UserChange.JIDs, false)
//	case whatsapp.ChatActionAnnounce:
//		go portal.RestrictMessageSending(cmd.Data.Announce)
//	case whatsapp.ChatActionRestrict:
//		go portal.RestrictMetadataChanges(cmd.Data.Restrict)
//	case whatsapp.ChatActionRemove:
//		go portal.HandleWhatsAppKick(nil, cmd.Data.SenderJID, cmd.Data.UserChange.JIDs)
//	case whatsapp.ChatActionAdd:
//		go portal.HandleWhatsAppInvite(user, cmd.Data.SenderJID, nil, cmd.Data.UserChange.JIDs)
//	case whatsapp.ChatActionIntroduce:
//		if cmd.Data.SenderJID != "unknown" {
//			go portal.Sync(user, whatsapp.Contact{JID: portal.Key.JID})
//		}
//	}
//}
//
//func (user *User) HandleJSONMessage(evt whatsapp.RawJSONMessage) {
//	if !json.Valid(evt.RawMessage) {
//		return
//	}
//	user.log.Debugfln("JSON message with tag %s: %s", evt.Tag, evt.RawMessage)
//	user.updateLastConnectionIfNecessary()
//}

func (user *User) NeedsRelaybot(portal *Portal) bool {
	return !user.HasSession() // || !user.IsInPortal(portal.Key)
}
