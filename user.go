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
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/skip2/go-qrcode"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/format"

	"github.com/Rhymen/go-whatsapp"
	waProto "github.com/Rhymen/go-whatsapp/binary/proto"

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
	Connected   bool

	ConnectionErrors int

	cleanDisconnection bool

	messages chan PortalMessage
	syncLock sync.Mutex
}

func (bridge *Bridge) GetUserByMXID(userID types.MatrixUserID) *User {
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

func (bridge *Bridge) loadDBUser(dbUser *database.User, mxid *types.MatrixUserID) *User {
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
	defer user.bridge.portalsLock.Unlock()

	for i, key := range keys {
		portal, ok := user.bridge.portalsByJID[key]
		if !ok {
			portal = user.bridge.loadDBPortal(user.bridge.DB.Portal.GetByJID(key), &key)
		}
		portals[i] = portal
	}
	return portals
}

func (bridge *Bridge) NewUser(dbUser *database.User) *User {
	user := &User{
		User:   dbUser,
		bridge: bridge,
		log:    bridge.Log.Sub("User").Sub(string(dbUser.MXID)),

		messages: make(chan PortalMessage, 256),
	}
	user.Whitelisted = user.bridge.Config.Bridge.Permissions.IsWhitelisted(user.MXID)
	user.Admin = user.bridge.Config.Bridge.Permissions.IsAdmin(user.MXID)
	go user.handleMessageLoop()
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
		msg := format.RenderMarkdown(fmt.Sprintf("\u26a0 Failed to connect to WhatsApp server. " +
			"This indicates a network problem on the bridge server. See bridge logs for more info."))
		_, _ = user.bridge.Bot.SendMessageEvent(user.ManagementRoom, mautrix.EventMessage, msg)
		return false
	}
	user.Conn = whatsappExt.ExtendConn(conn)
	_ = user.Conn.SetClientName("Mautrix-WhatsApp bridge", "mx-wa")
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
			msg := format.RenderMarkdown(fmt.Sprintf("\u26a0 Failed to connect to WhatsApp. Make sure WhatsApp "+
				"on your phone is reachable and use `%s reconnect` to try connecting again.",
				user.bridge.Config.Bridge.CommandPrefix))
			_, _ = user.bridge.Bot.SendMessageEvent(user.ManagementRoom, mautrix.EventMessage, msg)
			return false
		}
		user.Connected = true
		user.ConnectionErrors = 0
		user.SetSession(&sess)
		user.log.Debugln("Session restored successfully")
		user.PostLogin()
	}
	return true
}

func (user *User) IsLoggedIn() bool {
	return user.Session != nil || user.Conn != nil
}

func (user *User) Login(ce *CommandEvent) {
	qrChan := make(chan string, 2)
	go func() {
		code := <-qrChan
		if code == "error" {
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

		_, err = bot.SendImage(ce.RoomID, string(code), resp.ContentURI)
		if err != nil {
			user.log.Errorln("Failed to send QR code to user:", err)
		}
	}()
	session, err := user.Conn.Login(qrChan)
	if err != nil {
		qrChan <- "error"
		if err == whatsapp.ErrAlreadyLoggedIn {
			ce.Reply("You're already logged in.")
		} else if err == whatsapp.ErrLoginInProgress {
			ce.Reply("You have a login in progress already.")
		} else if err.Error() == "qr code scan timed out" {
			ce.Reply("QR code scan timed out. Please try again.")
		} else {
			user.log.Warnln("Failed to log in:", err)
			ce.Reply("Unknown error while logging in: %v", err)
		}
		return
	}
	user.Connected = true
	user.ConnectionErrors = 0
	user.JID = strings.Replace(user.Conn.Info.Wid, whatsappExt.OldUserSuffix, whatsappExt.NewUserSuffix, 1)
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
	user.syncLock.Lock()
	go user.intPostLogin()
}

func (user *User) intPostLogin() {
	dur := time.Duration(user.bridge.Config.Bridge.ContactWaitDelay) * time.Second
	user.log.Debugfln("Waiting %s for contacts to arrive", dur)
	// Hacky way to wait for chats and contacts to arrive automatically
	time.Sleep(dur)
	user.log.Debugfln("Waited %s, have %d chats and %d contacts", dur, len(user.Conn.Store.Chats), len(user.Conn.Store.Contacts))

	go user.syncPuppets()
	user.syncPortals(false)
	user.syncLock.Unlock()
}

func (user *User) syncPortals(createAll bool) {
	user.log.Infoln("Reading chat list")
	chats := make(ChatList, 0, len(user.Conn.Store.Chats))
	portalKeys := make([]database.PortalKey, 0, len(user.Conn.Store.Chats))
	for _, chat := range user.Conn.Store.Chats {
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
		portalKeys = append(portalKeys, portal.Key)
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
	user.log.Infoln("Finished syncing portals")
}

func (user *User) syncPuppets() {
	user.log.Infoln("Syncing puppet info from contacts")
	for jid, contact := range user.Conn.Store.Contacts {
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
	if errors.Cause(err) != whatsapp.ErrInvalidWsData {
		user.log.Errorln("WhatsApp error:", err)
	}
	if closed, ok := err.(*whatsapp.ErrConnectionClosed); ok {
		user.Connected = false
		if closed.Code == 1000 && user.cleanDisconnection {
			user.cleanDisconnection = false
			user.log.Infoln("Clean disconnection by server")
			return
		}
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection was closed with websocket status code %d", closed.Code))
	} else if failed, ok := err.(*whatsapp.ErrConnectionFailed); ok {
		user.Connected = false
		user.ConnectionErrors++
		go user.tryReconnect(fmt.Sprintf("Your WhatsApp connection failed: %v", failed.Err))
	}
	// Otherwise unknown error, probably mostly harmless
}

func (user *User) tryReconnect(msg string) {
	if user.ConnectionErrors > user.bridge.Config.Bridge.MaxConnectionAttempts {
		content := format.RenderMarkdown(fmt.Sprintf("%s. Use the `reconnect` command to reconnect.", msg))
		_, _ = user.bridge.Bot.SendMessageEvent(user.ManagementRoom, mautrix.EventMessage, content)
		return
	}
	if user.bridge.Config.Bridge.ReportConnectionRetry {
		_, _ = user.bridge.Bot.SendNotice(user.ManagementRoom, fmt.Sprintf("%s. Reconnecting...", msg))
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
			user.Connected = true
			_, _ = user.bridge.Bot.SendNotice(user.ManagementRoom, "Reconnected successfully")
			user.PostLogin()
			return
		}
		user.log.Errorln("Error while trying to reconnect after disconnection:", err)
		tries++
		user.ConnectionErrors++
		if user.ConnectionErrors <= user.bridge.Config.Bridge.MaxConnectionAttempts {
			if exponentialBackoff {
				delay = (1 << tries) + baseDelay
			}
			if user.bridge.Config.Bridge.ReportConnectionRetry {
				_, _ = user.bridge.Bot.SendNotice(user.ManagementRoom,
					fmt.Sprintf("Reconnection attempt failed: %v. Retrying in %d seconds...", err, delay))
			}
			time.Sleep(delay * time.Second)
		}
	}

	if user.bridge.Config.Bridge.ReportConnectionRetry {
		msg = fmt.Sprintf("%d reconnection attempts failed. Use the `reconnect` command to try to reconnect manually.", tries)
	} else {
		msg = fmt.Sprintf("\u26a0 %sAdditionally, %d reconnection attempts failed. "+
			"Use the `reconnect` command to try to reconnect.", msg, tries)
	}

	content := format.RenderMarkdown(msg)
	_, _ = user.bridge.Bot.SendMessageEvent(user.ManagementRoom, mautrix.EventMessage, content)
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
	for msg := range user.messages {
		user.syncLock.Lock()
		user.GetPortalByJID(msg.chat).messages <- msg
		user.syncLock.Unlock()
	}
}

func (user *User) putMessage(message PortalMessage) {
	select {
	case user.messages <- message:
	default:
		user.log.Warnln("Buffer is full, dropping message in", message.chat)
	}
}

func (user *User) HandleTextMessage(message whatsapp.TextMessage) {
	user.putMessage(PortalMessage{message.Info.RemoteJid, user, message, message.Info.Timestamp})
}

func (user *User) HandleImageMessage(message whatsapp.ImageMessage) {
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

func (user *User) HandleMessageRevoke(message whatsappExt.MessageRevocation) {
	user.putMessage(PortalMessage{message.RemoteJid, user, message, 0})
}

type FakeMessage struct {
	Text string
	ID   string
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
		data.Text = "Incoming call"
	case whatsappExt.CallOfferVideo:
		data.Text = "Incoming video call"
	case whatsappExt.CallTerminate:
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
		}()
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
			go portal.UpdateAvatar(user, cmd.ProfilePicInfo)
		}
	case whatsappExt.CommandDisconnect:
		var msg string
		if cmd.Kind == "replaced" {
			msg = "\u26a0 Your WhatsApp connection was closed by the server because you opened another WhatsApp Web client.\n\n" +
				"Use the `reconnect` command to disconnect the other client and resume bridging."
		} else {
			user.log.Warnln("Unknown kind of disconnect:", string(cmd.Raw))
			msg = fmt.Sprintf("\u26a0 Your WhatsApp connection was closed by the server (reason code: %s).\n\n"+
				"Use the `reconnect` command to reconnect.", cmd.Kind)
		}
		user.cleanDisconnection = true
		go user.bridge.Bot.SendMessageEvent(user.ManagementRoom, mautrix.EventMessage, format.RenderMarkdown(msg))
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
		go portal.UpdateName(cmd.Data.NameChange.Name, cmd.Data.SenderJID)
	case whatsappExt.ChatActionAddTopic:
		go portal.UpdateTopic(cmd.Data.AddTopic.Topic, cmd.Data.SenderJID)
	case whatsappExt.ChatActionRemoveTopic:
		go portal.UpdateTopic("", cmd.Data.SenderJID)
	case whatsappExt.ChatActionPromote:
		go portal.ChangeAdminStatus(cmd.Data.PermissionChange.JIDs, true)
	case whatsappExt.ChatActionDemote:
		go portal.ChangeAdminStatus(cmd.Data.PermissionChange.JIDs, false)
	case whatsappExt.ChatActionAnnounce:
		go portal.RestrictMessageSending(cmd.Data.Announce)
	case whatsappExt.ChatActionRestrict:
		go portal.RestrictMetadataChanges(cmd.Data.Restrict)
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
