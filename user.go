// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"maunium.net/go/mautrix-whatsapp/database"
)

type User struct {
	*database.User
	Client  *whatsmeow.Client
	Session *store.Device

	bridge *Bridge
	log    log.Logger

	Admin            bool
	Whitelisted      bool
	RelayWhitelisted bool

	mgmtCreateLock  sync.Mutex
	spaceCreateLock sync.Mutex
	connLock        sync.Mutex

	historySyncs     chan *events.HistorySync
	prevBridgeStatus *BridgeState
	lastPresence     types.Presence

	spaceMembershipChecked  bool
	lastPhoneOfflineWarning time.Time

	groupListCache     []*types.GroupInfo
	groupListCacheLock sync.Mutex
	groupListCacheTime time.Time
}

func (bridge *Bridge) getUserByMXID(userID id.UserID, onlyIfExists bool) *User {
	_, isPuppet := bridge.ParsePuppetMXID(userID)
	if isPuppet || userID == bridge.Bot.UserID {
		return nil
	}
	bridge.usersLock.Lock()
	defer bridge.usersLock.Unlock()
	user, ok := bridge.usersByMXID[userID]
	if !ok {
		userIDPtr := &userID
		if onlyIfExists {
			userIDPtr = nil
		}
		return bridge.loadDBUser(bridge.DB.User.GetByMXID(userID), userIDPtr)
	}
	return user
}

func (bridge *Bridge) GetUserByMXID(userID id.UserID) *User {
	return bridge.getUserByMXID(userID, false)
}

func (bridge *Bridge) GetUserByMXIDIfExists(userID id.UserID) *User {
	return bridge.getUserByMXID(userID, true)
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

func (user *User) removeFromJIDMap(state BridgeState) {
	user.bridge.usersLock.Lock()
	jidUser, ok := user.bridge.usersByUsername[user.JID.User]
	if ok && user == jidUser {
		delete(user.bridge.usersByUsername, user.JID.User)
	}
	user.bridge.usersLock.Unlock()
	user.bridge.Metrics.TrackLoginState(user.JID, false)
	user.sendBridgeState(state)
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
			user.log.Errorfln("Failed to load user's whatsapp session: %v", err)
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

func (bridge *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: bridge,
		log:    bridge.Log.Sub("User").Sub(string(dbUser.MXID)),

		historySyncs: make(chan *events.HistorySync, 32),
		lastPresence: types.PresenceUnavailable,
	}
	user.RelayWhitelisted = user.bridge.Config.Bridge.Permissions.IsRelayWhitelisted(user.MXID)
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.MXID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.MXID)
	go user.handleHistorySyncsLoop()
	return user
}

func (user *User) ensureInvited(intent *appservice.IntentAPI, roomID id.RoomID, isDirect bool) (ok bool) {
	inviteContent := event.Content{
		Parsed: &event.MemberEventContent{
			Membership: event.MembershipInvite,
			IsDirect:   isDirect,
		},
		Raw: map[string]interface{}{},
	}
	customPuppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		inviteContent.Raw["fi.mau.will_auto_accept"] = true
	}
	_, err := intent.SendStateEvent(roomID, event.StateMember, user.MXID.String(), &inviteContent)
	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		user.bridge.StateStore.SetMembership(roomID, user.MXID, event.MembershipJoin)
		ok = true
		return
	} else if err != nil {
		user.log.Warnfln("Failed to invite user to %s: %v", roomID, err)
	} else {
		ok = true
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(roomID, appservice.EnsureJoinedParams{IgnoreCache: true})
		if err != nil {
			user.log.Warnfln("Failed to auto-join %s: %v", roomID, err)
			ok = false
		} else {
			ok = true
		}
	}
	return
}

func (user *User) GetSpaceRoom() id.RoomID {
	if !user.bridge.Config.Bridge.PersonalFilteringSpaces {
		return ""
	}

	if len(user.SpaceRoom) == 0 {
		user.spaceCreateLock.Lock()
		defer user.spaceCreateLock.Unlock()
		if len(user.SpaceRoom) > 0 {
			return user.SpaceRoom
		}

		resp, err := user.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Visibility: "private",
			Name:       "WhatsApp",
			Topic:      "Your WhatsApp bridged chats",
			InitialState: []*event.Event{{
				Type: event.StateRoomAvatar,
				Content: event.Content{
					Parsed: &event.RoomAvatarEventContent{
						URL: user.bridge.Config.AppService.Bot.ParsedAvatar,
					},
				},
			}},
			CreationContent: map[string]interface{}{
				"type": event.RoomTypeSpace,
			},
			PowerLevelOverride: &event.PowerLevelsEventContent{
				Users: map[id.UserID]int{
					user.bridge.Bot.UserID: 9001,
					user.MXID:              50,
				},
			},
		})

		if err != nil {
			user.log.Errorln("Failed to auto-create space room:", err)
		} else {
			user.SpaceRoom = resp.RoomID
			user.Update()
			user.ensureInvited(user.bridge.Bot, user.SpaceRoom, false)
		}
	} else if !user.spaceMembershipChecked && !user.bridge.StateStore.IsInRoom(user.SpaceRoom, user.MXID) {
		user.ensureInvited(user.bridge.Bot, user.SpaceRoom, false)
	}
	user.spaceMembershipChecked = true

	return user.SpaceRoom
}

func (user *User) GetManagementRoom() id.RoomID {
	if len(user.ManagementRoom) == 0 {
		user.mgmtCreateLock.Lock()
		defer user.mgmtCreateLock.Unlock()
		if len(user.ManagementRoom) > 0 {
			return user.ManagementRoom
		}
		creationContent := make(map[string]interface{})
		if !user.bridge.Config.Bridge.FederateRooms {
			creationContent["m.federate"] = false
		}
		resp, err := user.bridge.Bot.CreateRoom(&mautrix.ReqCreateRoom{
			Topic:           "WhatsApp bridge notices",
			IsDirect:        true,
			CreationContent: creationContent,
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

var ErrAlreadyLoggedIn = errors.New("already logged in")

func (user *User) createClient(sess *store.Device) {
	user.Client = whatsmeow.NewClient(sess, &waLogger{user.log.Sub("Client")})
	user.Client.AddEventHandler(user.HandleEvent)
	user.Client.SetForceActiveDeliveryReceipts(user.bridge.Config.Bridge.ForceActiveDeliveryReceipts)
	user.Client.GetMessageForRetry = func(to types.JID, id types.MessageID) *waProto.Message {
		user.bridge.Metrics.TrackRetryReceipt(0, false)
		return nil
	}
	user.Client.PreRetryCallback = func(receipt *events.Receipt, retryCount int, msg *waProto.Message) bool {
		user.bridge.Metrics.TrackRetryReceipt(retryCount, true)
		return true
	}
}

func (user *User) Login(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	user.connLock.Lock()
	defer user.connLock.Unlock()
	if user.Session != nil {
		return nil, ErrAlreadyLoggedIn
	} else if user.Client != nil {
		user.unlockedDeleteConnection()
	}
	newSession := user.bridge.WAContainer.NewDevice()
	newSession.Log = &waLogger{user.log.Sub("Session")}
	user.createClient(newSession)
	qrChan, err := user.Client.GetQRChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get QR channel: %w", err)
	}
	err = user.Client.Connect()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to WhatsApp: %w", err)
	}
	return qrChan, nil
}

func (user *User) Connect() bool {
	user.connLock.Lock()
	defer user.connLock.Unlock()
	if user.Client != nil {
		return user.Client.IsConnected()
	} else if user.Session == nil {
		return false
	}
	user.log.Debugln("Connecting to WhatsApp")
	user.sendBridgeState(BridgeState{StateEvent: StateConnecting, Error: WAConnecting})
	user.createClient(user.Session)
	err := user.Client.Connect()
	if err != nil {
		user.log.Warnln("Error connecting to WhatsApp:", err)
		return false
	}
	return true
}

func (user *User) unlockedDeleteConnection() {
	if user.Client == nil {
		return
	}
	user.Client.Disconnect()
	user.Client.RemoveEventHandlers()
	user.Client = nil
	user.bridge.Metrics.TrackConnectionState(user.JID, false)
}

func (user *User) DeleteConnection() {
	user.connLock.Lock()
	defer user.connLock.Unlock()
	user.unlockedDeleteConnection()
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

func (user *User) IsConnected() bool {
	return user.Client != nil && user.Client.IsConnected()
}

func (user *User) IsLoggedIn() bool {
	return user.IsConnected() && user.Client.IsLoggedIn()
}

func (user *User) tryAutomaticDoublePuppeting() {
	if !user.bridge.Config.CanAutoDoublePuppet(user.MXID) {
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

func (user *User) sendMarkdownBridgeAlert(formatString string, args ...interface{}) {
	if user.bridge.Config.Bridge.DisableBridgeAlerts {
		return
	}
	notice := fmt.Sprintf(formatString, args...)
	content := format.RenderMarkdown(notice, true, false)
	_, err := user.bridge.Bot.SendMessageEvent(user.GetManagementRoom(), event.EventMessage, content)
	if err != nil {
		user.log.Warnf("Failed to send bridge alert \"%s\": %v", notice, err)
	}
}

const callEventMaxAge = 15 * time.Minute

func (user *User) handleCallStart(sender types.JID, id, callType string, ts time.Time) {
	if !user.bridge.Config.Bridge.CallStartNotices || ts.Add(callEventMaxAge).Before(time.Now()) {
		return
	}
	portal := user.GetPortalByJID(sender)
	text := "Incoming call"
	if callType != "" {
		text = fmt.Sprintf("Incoming %s call", callType)
	}
	portal.messages <- PortalMessage{
		fake: &fakeMessage{
			Sender:    sender,
			Text:      text,
			ID:        id,
			Time:      ts,
			Important: true,
		},
		source: user,
	}
}

const PhoneDisconnectWarningTime = 12 * 24 * time.Hour // 12 days
const PhoneDisconnectPingTime = 10 * 24 * time.Hour
const PhoneMinPingInterval = 24 * time.Hour

func (user *User) sendHackyPhonePing() {
	msgID := whatsmeow.GenerateMessageID()
	user.PhoneLastPinged = time.Now()
	ts, err := user.Client.SendMessage(user.JID.ToNonAD(), msgID, &waProto.Message{
		ProtocolMessage: &waProto.ProtocolMessage{},
	})
	if err != nil {
		user.log.Warnfln("Failed to send hacky phone ping: %v", err)
	} else {
		user.log.Debugfln("Sent hacky phone ping %s/%s because phone has been offline for >10 days", msgID, ts)
		user.PhoneLastPinged = ts
		user.Update()
	}
}

func (user *User) PhoneRecentlySeen(doPing bool) bool {
	if doPing && !user.PhoneLastSeen.IsZero() && user.PhoneLastSeen.Add(PhoneDisconnectPingTime).Before(time.Now()) && user.PhoneLastPinged.Add(PhoneMinPingInterval).Before(time.Now()) {
		// Over 10 days since the phone was seen and over a day since the last somewhat hacky ping, send a new ping.
		go user.sendHackyPhonePing()
	}
	return user.PhoneLastSeen.IsZero() || user.PhoneLastSeen.Add(PhoneDisconnectWarningTime).After(time.Now())
}

// phoneSeen records a timestamp when the user's main device was seen online.
// The stored timestamp can later be used to warn the user if the main device is offline for too long.
func (user *User) phoneSeen(ts time.Time) {
	if user.PhoneLastSeen.Add(1 * time.Hour).After(ts) {
		// The last seen timestamp isn't going to be perfectly accurate in any case,
		// so don't spam the database with an update every time there's an event.
		return
	} else if !user.PhoneRecentlySeen(false) && user.GetPrevBridgeState().Error == WAPhoneOffline && user.IsConnected() {
		user.log.Debugfln("Saw phone after current bridge state said it has been offline, switching state back to connected")
		go user.sendBridgeState(BridgeState{StateEvent: StateConnected})
	}
	user.PhoneLastSeen = ts
	go user.Update()
}

func formatDisconnectTime(dur time.Duration) string {
	days := int(math.Floor(dur.Hours() / 24))
	hours := int(dur.Hours()) % 24
	if hours == 0 {
		return fmt.Sprintf("%d days", days)
	} else if hours == 1 {
		return fmt.Sprintf("%d days and 1 hour", days)
	} else {
		return fmt.Sprintf("%d days and %d hours", days, hours)
	}
}

func (user *User) sendPhoneOfflineWarning() {
	if user.lastPhoneOfflineWarning.Add(12 * time.Hour).After(time.Now()) {
		// Don't spam the warning too much
		return
	}
	user.lastPhoneOfflineWarning = time.Now()
	timeSinceSeen := time.Now().Sub(user.PhoneLastSeen)
	user.sendMarkdownBridgeAlert("Your phone hasn't been seen in %s. The server will force the bridge to log out if the phone is not active at least every 2 weeks.", formatDisconnectTime(timeSinceSeen))
}

func (user *User) HandleEvent(event interface{}) {
	switch v := event.(type) {
	case *events.LoggedOut:
		go user.handleLoggedOut(v.OnConnect, v.Reason)
	case *events.Connected:
		user.bridge.Metrics.TrackConnectionState(user.JID, true)
		user.bridge.Metrics.TrackLoginState(user.JID, true)
		if len(user.Client.Store.PushName) > 0 {
			go func() {
				err := user.Client.SendPresence(user.lastPresence)
				if err != nil {
					user.log.Warnln("Failed to send initial presence:", err)
				}
			}()
		}
		go user.tryAutomaticDoublePuppeting()
	case *events.OfflineSyncPreview:
		user.log.Infofln("Server says it's going to send %d messages and %d receipts that were missed during downtime", v.Messages, v.Receipts)
		go user.sendBridgeState(BridgeState{
			StateEvent: StateBackfilling,
			Message:    fmt.Sprintf("backfilling %d messages and %d receipts", v.Messages, v.Receipts),
		})
	case *events.OfflineSyncCompleted:
		if !user.PhoneRecentlySeen(true) {
			user.log.Infofln("Offline sync completed, but phone last seen date is still %s - sending phone offline bridge status", user.PhoneLastSeen)
			go user.sendBridgeState(BridgeState{StateEvent: StateTransientDisconnect, Error: WAPhoneOffline})
		} else {
			if user.GetPrevBridgeState().StateEvent == StateBackfilling {
				user.log.Infoln("Offline sync completed")
			}
			go user.sendBridgeState(BridgeState{StateEvent: StateConnected})
		}
	case *events.AppStateSyncComplete:
		if len(user.Client.Store.PushName) > 0 && v.Name == appstate.WAPatchCriticalBlock {
			err := user.Client.SendPresence(user.lastPresence)
			if err != nil {
				user.log.Warnln("Failed to send presence after app state sync:", err)
			}
		} else if v.Name == appstate.WAPatchCriticalUnblockLow {
			go func() {
				err := user.ResyncContacts()
				if err != nil {
					user.log.Errorln("Failed to resync puppets: %v", err)
				}
			}()
		}
	case *events.PushNameSetting:
		// Send presence available when connecting and when the pushname is changed.
		// This makes sure that outgoing messages always have the right pushname.
		err := user.Client.SendPresence(user.lastPresence)
		if err != nil {
			user.log.Warnln("Failed to send presence after push name update:", err)
		}
	case *events.PairSuccess:
		user.PhoneLastSeen = time.Now()
		user.Session = user.Client.Store
		user.JID = v.ID
		user.addToJIDMap()
		user.Update()
	case *events.StreamError:
		var message string
		if v.Code != "" {
			message = fmt.Sprintf("Unknown stream error with code %s", v.Code)
		} else if children := v.Raw.GetChildren(); len(children) > 0 {
			message = fmt.Sprintf("Unknown stream error (contains %s node)", children[0].Tag)
		} else {
			message = "Unknown stream error"
		}
		go user.sendBridgeState(BridgeState{StateEvent: StateUnknownError, Message: message})
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.ConnectFailure:
		go user.sendBridgeState(BridgeState{StateEvent: StateUnknownError, Message: fmt.Sprintf("Unknown connection failure: %s", v.Reason)})
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.ClientOutdated:
		user.log.Errorfln("Got a client outdated connect failure. The bridge is likely out of date, please update immediately.")
		go user.sendBridgeState(BridgeState{StateEvent: StateUnknownError, Message: "Connect failure: 405 client outdated"})
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.TemporaryBan:
		go user.sendBridgeState(BridgeState{StateEvent: StateBadCredentials, Message: v.String()})
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.Disconnected:
		// Don't send the normal transient disconnect state if we're already in a different transient disconnect state.
		// TODO remove this if/when the phone offline state is moved to a sub-state of CONNECTED
		if user.GetPrevBridgeState().Error != WAPhoneOffline && user.PhoneRecentlySeen(false) {
			go user.sendBridgeState(BridgeState{StateEvent: StateTransientDisconnect, Message: "Disconnected from WhatsApp. Trying to reconnect."})
		}
		user.bridge.Metrics.TrackConnectionState(user.JID, false)
	case *events.Contact:
		go user.syncPuppet(v.JID, "contact event")
	case *events.PushName:
		go user.syncPuppet(v.JID, "push name event")
	case *events.GroupInfo:
		user.groupListCache = nil
		go user.handleGroupUpdate(v)
	case *events.JoinedGroup:
		user.groupListCache = nil
		go user.handleGroupCreate(v)
	case *events.Picture:
		go user.handlePictureUpdate(v)
	case *events.Receipt:
		if v.IsFromMe && v.Sender.Device == 0 {
			user.phoneSeen(v.Timestamp)
		}
		go user.handleReceipt(v)
	case *events.ChatPresence:
		go user.handleChatPresence(v)
	case *events.Message:
		portal := user.GetPortalByMessageSource(v.Info.MessageSource)
		portal.messages <- PortalMessage{evt: v, source: user}
	case *events.MediaRetry:
		user.phoneSeen(v.Timestamp)
		portal := user.GetPortalByJID(v.ChatID)
		portal.mediaRetries <- PortalMediaRetry{evt: v, source: user}
	case *events.CallOffer:
		user.handleCallStart(v.CallCreator, v.CallID, "", v.Timestamp)
	case *events.CallOfferNotice:
		user.handleCallStart(v.CallCreator, v.CallID, v.Type, v.Timestamp)
	case *events.IdentityChange:
		puppet := user.bridge.GetPuppetByJID(v.JID)
		portal := user.GetPortalByJID(v.JID)
		if len(portal.MXID) > 0 && user.bridge.Config.Bridge.IdentityChangeNotices {
			text := fmt.Sprintf("Your security code with %s changed.", puppet.Displayname)
			if v.Implicit {
				text = fmt.Sprintf("Your security code with %s (device #%d) changed.", puppet.Displayname, v.JID.Device)
			}
			portal.messages <- PortalMessage{
				fake: &fakeMessage{
					Sender:    v.JID,
					Text:      text,
					ID:        strconv.FormatInt(v.Timestamp.Unix(), 10),
					Time:      v.Timestamp,
					Important: false,
				},
				source: user,
			}
		}
	case *events.CallTerminate, *events.CallRelayLatency, *events.CallAccept, *events.UnknownCallEvent:
		// ignore
	case *events.UndecryptableMessage:
		portal := user.GetPortalByMessageSource(v.Info.MessageSource)
		portal.messages <- PortalMessage{undecryptable: v, source: user}
	case *events.HistorySync:
		user.historySyncs <- v
	case *events.Mute:
		portal := user.GetPortalByJID(v.JID)
		if portal != nil {
			var mutedUntil time.Time
			if v.Action.GetMuted() {
				mutedUntil = time.Unix(v.Action.GetMuteEndTimestamp(), 0)
			}
			go user.updateChatMute(nil, portal, mutedUntil)
		}
	case *events.Archive:
		portal := user.GetPortalByJID(v.JID)
		if portal != nil {
			go user.updateChatTag(nil, portal, user.bridge.Config.Bridge.ArchiveTag, v.Action.GetArchived())
		}
	case *events.Pin:
		portal := user.GetPortalByJID(v.JID)
		if portal != nil {
			go user.updateChatTag(nil, portal, user.bridge.Config.Bridge.PinnedTag, v.Action.GetPinned())
		}
	case *events.AppState:
		// Ignore
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
		user.log.Debugfln("Portal %s is muted until %s, unmuting...", portal.MXID, mutedUntil)
		err = intent.DeletePushRule("global", pushrules.RoomRule, string(portal.MXID))
	} else {
		user.log.Debugfln("Portal %s is muted until %s, muting...", portal.MXID, mutedUntil)
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
	DoublePuppet string      `json:"fi.mau.double_puppet_source"`
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
		data := CustomTagData{"0.5", doublePuppetValue}
		err = intent.AddTagWithCustomData(portal.MXID, tag, &data)
	} else if !active && ok && currentTag.DoublePuppet == doublePuppetValue {
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
	Timestamp          int64  `json:"ts,omitempty"`
	DoublePuppetSource string `json:"fi.mau.double_puppet_source,omitempty"`
}

type CustomReadMarkers struct {
	mautrix.ReqSetReadMarkers
	ReadExtra      CustomReadReceipt `json:"com.beeper.read.extra"`
	FullyReadExtra CustomReadReceipt `json:"com.beeper.fully_read.extra"`
}

func (user *User) syncChatDoublePuppetDetails(portal *Portal, justCreated bool) {
	doublePuppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if doublePuppet == nil {
		return
	}
	if doublePuppet == nil || doublePuppet.CustomIntent() == nil || len(portal.MXID) == 0 {
		return
	}
	if justCreated || !user.bridge.Config.Bridge.TagOnlyOnCreate {
		chat, err := user.Client.Store.ChatSettings.GetChatSettings(portal.Key.JID)
		if err != nil {
			user.log.Warnfln("Failed to get settings of %s: %v", portal.Key.JID, err)
			return
		}
		intent := doublePuppet.CustomIntent()
		if portal.Key.JID == types.StatusBroadcastJID && justCreated && user.bridge.Config.Bridge.MuteStatusBroadcast {
			user.updateChatMute(intent, portal, time.Now().Add(365*24*time.Hour))
			user.updateChatTag(intent, portal, user.bridge.Config.Bridge.ArchiveTag, true)
			return
		} else if !chat.Found {
			return
		}
		user.updateChatMute(intent, portal, chat.MutedUntil)
		user.updateChatTag(intent, portal, user.bridge.Config.Bridge.ArchiveTag, chat.Archived)
		user.updateChatTag(intent, portal, user.bridge.Config.Bridge.PinnedTag, chat.Pinned)
	}
}

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

func (user *User) handleLoggedOut(onConnect bool, reason events.ConnectFailureReason) {
	errorCode := WAUnknownLogout
	if reason == events.ConnectFailureLoggedOut {
		errorCode = WALoggedOut
	} else if reason == events.ConnectFailureBanned {
		errorCode = WAAccountBanned
	}
	user.removeFromJIDMap(BridgeState{StateEvent: StateBadCredentials, Error: errorCode})
	user.DeleteConnection()
	user.Session = nil
	user.JID = types.EmptyJID
	user.Update()
	if onConnect {
		user.sendMarkdownBridgeAlert("Connecting to WhatsApp failed as the device was unlinked (error %s). Please link the bridge to your phone again.", reason)
	} else {
		user.sendMarkdownBridgeAlert("You were logged out from another device. Please link the bridge to your phone again.")
	}
}

func (user *User) GetPortalByMessageSource(ms types.MessageSource) *Portal {
	jid := ms.Chat
	if ms.IsIncomingBroadcast() {
		if ms.IsFromMe {
			jid = ms.BroadcastListOwner.ToNonAD()
		} else {
			jid = ms.Sender.ToNonAD()
		}
		if jid.IsEmpty() {
			return nil
		}
	}
	return user.bridge.GetPortalByJID(database.NewPortalKey(jid, user.JID))
}

func (user *User) GetPortalByJID(jid types.JID) *Portal {
	return user.bridge.GetPortalByJID(database.NewPortalKey(jid, user.JID))
}

func (user *User) syncPuppet(jid types.JID, reason string) {
	user.bridge.GetPuppetByJID(jid).SyncContact(user, false, reason)
}

func (user *User) ResyncContacts() error {
	contacts, err := user.Client.Store.Contacts.GetAllContacts()
	if err != nil {
		return fmt.Errorf("failed to get cached contacts: %w", err)
	}
	user.log.Infofln("Resyncing displaynames with %d contacts", len(contacts))
	for jid, contact := range contacts {
		puppet := user.bridge.GetPuppetByJID(jid)
		if puppet != nil {
			puppet.Sync(user, contact)
		} else {
			user.log.Warnfln("Got a nil puppet for %s while syncing contacts", jid)
		}
	}
	return nil
}

func (user *User) ResyncGroups(createPortals bool) error {
	groups, err := user.Client.GetJoinedGroups()
	if err != nil {
		return fmt.Errorf("failed to get group list from server: %w", err)
	}
	for _, group := range groups {
		portal := user.GetPortalByJID(group.JID)
		if len(portal.MXID) == 0 {
			if createPortals {
				err = portal.CreateMatrixRoom(user, group, true)
				if err != nil {
					return fmt.Errorf("failed to create room for %s: %w", group.JID, err)
				}
			}
		} else {
			portal.UpdateMatrixRoom(user, group)
		}
	}
	return nil
}

const WATypingTimeout = 15 * time.Second

func (user *User) handleChatPresence(presence *events.ChatPresence) {
	puppet := user.bridge.GetPuppetByJID(presence.Sender)
	portal := user.GetPortalByJID(presence.Chat)
	if puppet == nil || portal == nil || len(portal.MXID) == 0 {
		return
	}
	if presence.State == types.ChatPresenceComposing {
		if puppet.typingIn != "" && puppet.typingAt.Add(WATypingTimeout).Before(time.Now()) {
			if puppet.typingIn == portal.MXID {
				return
			}
			_, _ = puppet.IntentFor(portal).UserTyping(puppet.typingIn, false, 0)
		}
		_, _ = puppet.IntentFor(portal).UserTyping(portal.MXID, true, WATypingTimeout.Milliseconds())
		puppet.typingIn = portal.MXID
		puppet.typingAt = time.Now()
	} else {
		_, _ = puppet.IntentFor(portal).UserTyping(portal.MXID, false, 0)
		puppet.typingIn = ""
	}
}

func (user *User) handleReceipt(receipt *events.Receipt) {
	if receipt.Type != events.ReceiptTypeRead && receipt.Type != events.ReceiptTypeReadSelf {
		return
	}
	portal := user.GetPortalByMessageSource(receipt.MessageSource)
	if portal == nil || len(portal.MXID) == 0 {
		return
	}
	portal.receipts <- PortalReceipt{evt: receipt, source: user}
}

func makeReadMarkerContent(eventID id.EventID, doublePuppet bool) CustomReadMarkers {
	var extra CustomReadReceipt
	if doublePuppet {
		extra.DoublePuppetSource = doublePuppetValue
	}
	return CustomReadMarkers{
		ReqSetReadMarkers: mautrix.ReqSetReadMarkers{
			Read:      eventID,
			FullyRead: eventID,
		},
		ReadExtra:      extra,
		FullyReadExtra: extra,
	}
}

func (user *User) markSelfReadFull(portal *Portal) {
	puppet := user.bridge.GetPuppetByCustomMXID(user.MXID)
	if puppet == nil || puppet.CustomIntent() == nil {
		return
	}
	lastMessage := user.bridge.DB.Message.GetLastInChat(portal.Key)
	if lastMessage == nil {
		return
	}
	user.SetLastReadTS(portal.Key, lastMessage.Timestamp)
	err := puppet.CustomIntent().SetReadMarkers(portal.MXID, makeReadMarkerContent(lastMessage.MXID, true))
	if err != nil {
		user.log.Warnfln("Failed to mark %s (last message) in %s as read: %v", lastMessage.MXID, portal.MXID, err)
	} else {
		user.log.Debugfln("Marked %s (last message) in %s as read", lastMessage.MXID, portal.MXID)
	}
}

func (user *User) handleGroupCreate(evt *events.JoinedGroup) {
	portal := user.GetPortalByJID(evt.JID)
	if len(portal.MXID) == 0 {
		err := portal.CreateMatrixRoom(user, &evt.GroupInfo, true)
		if err != nil {
			user.log.Errorln("Failed to create Matrix room after join notification: %v", err)
		}
	} else {
		portal.UpdateMatrixRoom(user, &evt.GroupInfo)
	}
}

func (user *User) handleGroupUpdate(evt *events.GroupInfo) {
	portal := user.GetPortalByJID(evt.JID)
	if portal == nil || len(portal.MXID) == 0 {
		user.log.Debugfln("Ignoring group info update in chat with no portal: %+v", evt)
		return
	}
	switch {
	case evt.Announce != nil:
		portal.RestrictMessageSending(evt.Announce.IsAnnounce)
	case evt.Locked != nil:
		portal.RestrictMetadataChanges(evt.Locked.IsLocked)
	case evt.Name != nil:
		portal.UpdateName(evt.Name.Name, evt.Name.NameSetBy, true)
	case evt.Topic != nil:
		portal.UpdateTopic(evt.Topic.Topic, evt.Topic.TopicSetBy, true)
	case evt.Leave != nil:
		if evt.Sender != nil && !evt.Sender.IsEmpty() {
			portal.HandleWhatsAppKick(user, *evt.Sender, evt.Leave)
		}
	case evt.Join != nil:
		portal.HandleWhatsAppInvite(user, evt.Sender, evt.Join)
	case evt.Promote != nil:
		portal.ChangeAdminStatus(evt.Promote, true)
	case evt.Demote != nil:
		portal.ChangeAdminStatus(evt.Demote, false)
	case evt.Ephemeral != nil:
		portal.UpdateGroupDisappearingMessages(evt.Sender, evt.Timestamp, evt.Ephemeral.DisappearingTimer)
	}
}

func (user *User) handlePictureUpdate(evt *events.Picture) {
	if evt.JID.Server == types.DefaultUserServer {
		puppet := user.bridge.GetPuppetByJID(evt.JID)
		user.log.Debugfln("Received picture update for puppet %s (current: %s, new: %s)", evt.JID, puppet.Avatar, evt.PictureID)
		if puppet.Avatar != evt.PictureID {
			puppet.UpdateAvatar(user)
		}
	} else if portal := user.GetPortalByJID(evt.JID); portal != nil {
		user.log.Debugfln("Received picture update for portal %s (current: %s, new: %s)", evt.JID, portal.Avatar, evt.PictureID)
		if portal.Avatar != evt.PictureID {
			portal.UpdateAvatar(user, evt.Author, true)
		}
	}
}

func (user *User) StartPM(jid types.JID, reason string) (*Portal, *Puppet, bool, error) {
	user.log.Debugln("Starting PM with", jid, "from", reason)
	puppet := user.bridge.GetPuppetByJID(jid)
	puppet.SyncContact(user, true, reason)
	portal := user.GetPortalByJID(puppet.JID)
	if len(portal.MXID) > 0 {
		ok := portal.ensureUserInvited(user)
		if !ok {
			portal.log.Warnfln("ensureUserInvited(%s) returned false, creating new portal", user.MXID)
			portal.MXID = ""
		} else {
			return portal, puppet, false, nil
		}
	}
	err := portal.CreateMatrixRoom(user, nil, false)
	return portal, puppet, true, err
}

const groupListCacheMaxAge = 24 * time.Hour

func (user *User) getCachedGroupList() ([]*types.GroupInfo, error) {
	user.groupListCacheLock.Lock()
	defer user.groupListCacheLock.Unlock()
	if user.groupListCache != nil && user.groupListCacheTime.Add(groupListCacheMaxAge).After(time.Now()) {
		return user.groupListCache, nil
	}
	var err error
	user.groupListCache, err = user.Client.GetJoinedGroups()
	user.groupListCacheTime = time.Now()
	return user.groupListCache, err
}
