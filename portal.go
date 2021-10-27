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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"html"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/ioutil"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"

	"maunium.net/go/mautrix/format"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

const StatusBroadcastTopic = "WhatsApp status updates from your contacts"
const StatusBroadcastName = "WhatsApp Status Broadcast"
const BroadcastTopic = "WhatsApp broadcast list"
const UnnamedBroadcastName = "Unnamed broadcast list"
const PrivateChatTopic = "WhatsApp private chat"

var ErrStatusBroadcastDisabled = errors.New("status bridging is disabled")

func (bridge *Bridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	portal, ok := bridge.portalsByMXID[mxid]
	if !ok {
		return bridge.loadDBPortal(bridge.DB.Portal.GetByMXID(mxid), nil)
	}
	return portal
}

func (bridge *Bridge) GetPortalByJID(key database.PortalKey) *Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	portal, ok := bridge.portalsByJID[key]
	if !ok {
		return bridge.loadDBPortal(bridge.DB.Portal.GetByJID(key), &key)
	}
	return portal
}

func (bridge *Bridge) GetAllPortals() []*Portal {
	return bridge.dbPortalsToPortals(bridge.DB.Portal.GetAll())
}

func (bridge *Bridge) GetAllPortalsByJID(jid types.JID) []*Portal {
	return bridge.dbPortalsToPortals(bridge.DB.Portal.GetAllByJID(jid))
}

func (bridge *Bridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	bridge.portalsLock.Lock()
	defer bridge.portalsLock.Unlock()
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}
		portal, ok := bridge.portalsByJID[dbPortal.Key]
		if !ok {
			portal = bridge.loadDBPortal(dbPortal, nil)
		}
		output[index] = portal
	}
	return output
}

func (bridge *Bridge) loadDBPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	if dbPortal == nil {
		if key == nil {
			return nil
		}
		dbPortal = bridge.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}
	portal := bridge.NewPortal(dbPortal)
	bridge.portalsByJID[portal.Key] = portal
	if len(portal.MXID) > 0 {
		bridge.portalsByMXID[portal.MXID] = portal
	}
	return portal
}

func (portal *Portal) GetUsers() []*User {
	return nil
}

func (bridge *Bridge) NewManualPortal(key database.PortalKey) *Portal {
	portal := &Portal{
		Portal: bridge.DB.Portal.New(),
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Portal/%s", key)),

		messages: make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
	}
	portal.Key = key
	go portal.handleMessageLoop()
	return portal
}

func (bridge *Bridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal: dbPortal,
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Portal/%s", dbPortal.Key)),

		messages: make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
	}
	go portal.handleMessageLoop()
	return portal
}

const recentlyHandledLength = 100

type PortalMessage struct {
	evt           *events.Message
	undecryptable *events.UndecryptableMessage
	source        *User
}

type recentlyHandledWrapper struct {
	id  types.MessageID
	err bool
}

type Portal struct {
	*database.Portal

	bridge *Bridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex
	backfillLock   sync.Mutex

	recentlyHandled      [recentlyHandledLength]recentlyHandledWrapper
	recentlyHandledLock  sync.Mutex
	recentlyHandledIndex uint8

	privateChatBackfillInvitePuppet func()

	messages chan PortalMessage

	hasRelaybot *bool
}

func (portal *Portal) syncDoublePuppetDetailsAfterCreate(source *User) {
	doublePuppet := portal.bridge.GetPuppetByCustomMXID(source.MXID)
	if doublePuppet == nil {
		return
	}
	source.syncChatDoublePuppetDetails(doublePuppet, portal, true)
}

func (portal *Portal) handleMessageLoop() {
	for msg := range portal.messages {
		if len(portal.MXID) == 0 {
			if !portal.shouldCreateRoom(msg) {
				portal.log.Debugln("Not creating portal room for incoming message: message is not a chat message")
				continue
			}
			portal.log.Debugln("Creating Matrix room from incoming message")
			err := portal.CreateMatrixRoom(msg.source)
			if err != nil {
				portal.log.Errorln("Failed to create portal room:", err)
				continue
			}
			portal.syncDoublePuppetDetailsAfterCreate(msg.source)
		}
		if msg.evt != nil {
			portal.handleMessage(msg.source, msg.evt)
		} else if msg.undecryptable != nil {
			portal.handleUndecryptableMessage(msg.source, msg.undecryptable)
		} else {
			portal.log.Warnln("Unexpected PortalMessage with no message: %+v", msg)
		}
	}
}

func (portal *Portal) shouldCreateRoom(msg PortalMessage) bool {
	if msg.undecryptable != nil {
		return true
	}
	waMsg := msg.evt.Message
	supportedMessages := []interface{}{
		waMsg.Conversation,
		waMsg.ExtendedTextMessage,
		waMsg.ImageMessage,
		waMsg.StickerMessage,
		waMsg.VideoMessage,
		waMsg.AudioMessage,
		waMsg.VideoMessage,
		waMsg.DocumentMessage,
		waMsg.ContactMessage,
		waMsg.LocationMessage,
	}
	for _, message := range supportedMessages {
		if message != nil {
			return true
		}
	}
	return false
}

func (portal *Portal) getMessageType(waMsg *waProto.Message) string {
	switch {
	case waMsg == nil:
		return "ignore"
	case waMsg.Conversation != nil || waMsg.ExtendedTextMessage != nil:
		return "text"
	case waMsg.ImageMessage != nil:
		return fmt.Sprintf("image %s", waMsg.GetImageMessage().GetMimetype())
	case waMsg.StickerMessage != nil:
		return fmt.Sprintf("sticker %s", waMsg.GetStickerMessage().GetMimetype())
	case waMsg.VideoMessage != nil:
		return fmt.Sprintf("video %s", waMsg.GetVideoMessage().GetMimetype())
	case waMsg.AudioMessage != nil:
		return fmt.Sprintf("audio %s", waMsg.GetAudioMessage().GetMimetype())
	case waMsg.DocumentMessage != nil:
		return fmt.Sprintf("document %s", waMsg.GetDocumentMessage().GetMimetype())
	case waMsg.ContactMessage != nil:
		return "contact"
	case waMsg.LocationMessage != nil:
		return "location"
	case waMsg.GetProtocolMessage() != nil:
		switch waMsg.GetProtocolMessage().GetType() {
		case waProto.ProtocolMessage_REVOKE:
			return "revoke"
		case waProto.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE, waProto.ProtocolMessage_HISTORY_SYNC_NOTIFICATION, waProto.ProtocolMessage_INITIAL_SECURITY_NOTIFICATION_SETTING_SYNC:
			return "ignore"
		default:
			return "unknown_protocol"
		}
	default:
		return "unknown"
	}
}

func (portal *Portal) convertMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, waMsg *waProto.Message) *ConvertedMessage {
	switch {
	case waMsg.Conversation != nil || waMsg.ExtendedTextMessage != nil:
		return portal.convertTextMessage(intent, waMsg)
	case waMsg.ImageMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetImageMessage())
	case waMsg.StickerMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetStickerMessage())
	case waMsg.VideoMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetVideoMessage())
	case waMsg.AudioMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetAudioMessage())
	case waMsg.DocumentMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetDocumentMessage())
	case waMsg.ContactMessage != nil:
		return portal.convertContactMessage(intent, waMsg.GetContactMessage())
	case waMsg.LocationMessage != nil:
		return portal.convertLocationMessage(intent, waMsg.GetLocationMessage())
	default:
		return nil
	}
}

const UndecryptableMessageNotice = "Decrypting message from WhatsApp failed, waiting for sender to re-send... " +
	"([learn more](https://faq.whatsapp.com/general/security-and-privacy/seeing-waiting-for-this-message-this-may-take-a-while))"
var undecryptableMessageContent event.MessageEventContent

func init() {
	undecryptableMessageContent = format.RenderMarkdown(UndecryptableMessageNotice, true, false)
	undecryptableMessageContent.MsgType = event.MsgNotice
}

func (portal *Portal) handleUndecryptableMessage(source *User, evt *events.UndecryptableMessage) {
	if len(portal.MXID) == 0 {
		portal.log.Warnln("handleUndecryptableMessage called even though portal.MXID is empty")
		return
	} else if portal.isRecentlyHandled(evt.Info.ID, true) {
		portal.log.Debugfln("Not handling %s (undecryptable): message was recently handled", evt.Info.ID)
		return
	} else if existingMsg := portal.bridge.DB.Message.GetByJID(portal.Key, evt.Info.ID); existingMsg != nil {
		portal.log.Debugfln("Not handling %s (undecryptable): message is duplicate", evt.Info.ID)
		return
	}
	intent := portal.getMessageIntent(source, &evt.Info)
	content := undecryptableMessageContent
	resp, err := portal.sendMessage(intent, event.EventMessage, &content, evt.Info.Timestamp.UnixMilli())
	if err != nil {
		portal.log.Errorln("Failed to send decryption error of %s to Matrix: %v", evt.Info.ID, err)
	}
	portal.finishHandling(nil, &evt.Info, resp.EventID, true)
}

func (portal *Portal) handleMessage(source *User, evt *events.Message) {
	if len(portal.MXID) == 0 {
		portal.log.Warnln("handleMessage called even though portal.MXID is empty")
		return
	}
	msgID := evt.Info.ID
	msgType := portal.getMessageType(evt.Message)
	if msgType == "ignore" {
		return
	} else if portal.isRecentlyHandled(msgID, false) {
		portal.log.Debugfln("Not handling %s (%s): message was recently handled", msgID, msgType)
		return
	}
	existingMsg := portal.bridge.DB.Message.GetByJID(portal.Key, msgID)
	if existingMsg != nil {
		if existingMsg.DecryptionError {
			portal.log.Debugfln("Got decryptable version of previously undecryptable message %s (%s)", msgID, msgType)
		} else {
			portal.log.Debugfln("Not handling %s (%s): message is duplicate", msgID, msgType)
			return
		}
	}

	intent := portal.getMessageIntent(source, &evt.Info)
	converted := portal.convertMessage(intent, source, &evt.Info, evt.Message)
	if converted != nil {
		var eventID id.EventID
		if existingMsg != nil {
			converted.Content.SetEdit(existingMsg.MXID)
		}
		resp, err := portal.sendMessage(converted.Intent, converted.Type, converted.Content, evt.Info.Timestamp.UnixMilli())
		if err != nil {
			portal.log.Errorln("Failed to send %s to Matrix: %v", msgID, err)
		} else {
			eventID = resp.EventID
		}
		// TODO figure out how to handle captions with undecryptable messages turning decryptable
		if converted.Caption != nil && existingMsg == nil {
			resp, err = portal.sendMessage(converted.Intent, converted.Type, converted.Content, evt.Info.Timestamp.UnixMilli())
			if err != nil {
				portal.log.Errorln("Failed to send caption of %s to Matrix: %v", msgID, err)
			} else {
				eventID = resp.EventID
			}
		}
		if len(eventID) != 0 {
			portal.finishHandling(existingMsg, &evt.Info, resp.EventID, false)
		}
	} else if msgType == "revoke" {
		portal.HandleMessageRevoke(source, &evt.Info, evt.Message.GetProtocolMessage().GetKey())
		if existingMsg != nil {
			_, _ = portal.MainIntent().RedactEvent(portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
				Reason: "The undecryptable message was actually the deletion of another message",
			})
			existingMsg.UpdateMXID("net.maunium.whatsapp.fake::" + existingMsg.MXID, false)
		}
	} else {
		portal.log.Warnln("Unhandled message:", evt.Info, evt.Message)
		if existingMsg != nil {
			_, _ = portal.MainIntent().RedactEvent(portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
				Reason: "The undecryptable message contained an unsupported message type",
			})
			existingMsg.UpdateMXID("net.maunium.whatsapp.fake::" + existingMsg.MXID, false)
		}
		return
	}
	portal.bridge.Metrics.TrackWhatsAppMessage(evt.Info.Timestamp, strings.Split(msgType, " ")[0])
}

func (portal *Portal) isRecentlyHandled(id types.MessageID, decryptionError bool) bool {
	start := portal.recentlyHandledIndex
	lookingForMsg := recentlyHandledWrapper{id, decryptionError}
	for i := start; i != start; i = (i - 1) % recentlyHandledLength {
		if portal.recentlyHandled[i] == lookingForMsg {
			return true
		}
	}
	return false
}

func init() {
	gob.Register(&waProto.Message{})
}

func (portal *Portal) markHandled(msg *database.Message, info *types.MessageInfo, mxid id.EventID, isSent, recent, decryptionError bool) *database.Message {
	if msg == nil {
		msg = portal.bridge.DB.Message.New()
		msg.Chat = portal.Key
		msg.JID = info.ID
		msg.MXID = mxid
		msg.Timestamp = info.Timestamp
		msg.Sender = info.Sender
		msg.Sent = isSent
		msg.DecryptionError = decryptionError
		msg.Insert()
	} else {
		msg.UpdateMXID(mxid, decryptionError)
	}

	if recent {
		portal.recentlyHandledLock.Lock()
		index := portal.recentlyHandledIndex
		portal.recentlyHandledIndex = (portal.recentlyHandledIndex + 1) % recentlyHandledLength
		portal.recentlyHandledLock.Unlock()
		portal.recentlyHandled[index] = recentlyHandledWrapper{msg.JID, decryptionError}
	}
	return msg
}

func (portal *Portal) getMessagePuppet(user *User, info *types.MessageInfo) *Puppet {
	if info.IsFromMe {
		return portal.bridge.GetPuppetByJID(user.JID)
	} else if portal.IsPrivateChat() {
		return portal.bridge.GetPuppetByJID(portal.Key.JID)
	} else {
		puppet := portal.bridge.GetPuppetByJID(info.Sender)
		puppet.SyncContact(user, true)
		return puppet
	}
}

func (portal *Portal) getMessageIntent(user *User, info *types.MessageInfo) *appservice.IntentAPI {
	if info.IsFromMe {
		return portal.bridge.GetPuppetByJID(user.JID).IntentFor(portal)
	} else if portal.IsPrivateChat() {
		return portal.MainIntent()
	}
	puppet := portal.bridge.GetPuppetByJID(info.Sender)
	puppet.SyncContact(user, true)
	return puppet.IntentFor(portal)
}

func (portal *Portal) finishHandling(existing *database.Message, message *types.MessageInfo, mxid id.EventID, decryptionError bool) {
	portal.markHandled(existing, message, mxid, true, true, decryptionError)
	portal.sendDeliveryReceipt(mxid)
	if !decryptionError {
		portal.log.Debugln("Handled message", message.ID, "->", mxid)
	} else {
		portal.log.Debugln("Handled message", message.ID, "->", mxid, "(undecryptable message error notice)")
	}
}

func (portal *Portal) kickExtraUsers(participantMap map[types.JID]bool) {
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Warnln("Failed to get member list:", err)
		return
	}
	for member := range members.Joined {
		jid, ok := portal.bridge.ParsePuppetMXID(member)
		if ok {
			_, shouldBePresent := participantMap[jid]
			if !shouldBePresent {
				_, err = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{
					UserID: member,
					Reason: "User had left this WhatsApp chat",
				})
				if err != nil {
					portal.log.Warnfln("Failed to kick user %s who had left: %v", member, err)
				}
			}
		}
	}
}

//func (portal *Portal) SyncBroadcastRecipients(source *User, metadata *whatsapp.BroadcastListInfo) {
//	participantMap := make(map[whatsapp.JID]bool)
//	for _, recipient := range metadata.Recipients {
//		participantMap[recipient.JID] = true
//
//		puppet := portal.bridge.GetPuppetByJID(recipient.JID)
//		puppet.SyncContactIfNecessary(source)
//		err := puppet.DefaultIntent().EnsureJoined(portal.MXID)
//		if err != nil {
//			portal.log.Warnfln("Failed to make puppet of %s join %s: %v", recipient.JID, portal.MXID, err)
//		}
//	}
//	portal.kickExtraUsers(participantMap)
//}

func (portal *Portal) SyncParticipants(source *User, metadata *types.GroupInfo) {
	changed := false
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
		changed = true
	}
	participantMap := make(map[types.JID]bool)
	for _, participant := range metadata.Participants {
		participantMap[participant.JID] = true
		user := portal.bridge.GetUserByJID(participant.JID)
		portal.userMXIDAction(user, portal.ensureMXIDInvited)

		puppet := portal.bridge.GetPuppetByJID(participant.JID)
		puppet.SyncContact(source, true)
		err = puppet.IntentFor(portal).EnsureJoined(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to make puppet of %s join %s: %v", participant.JID, portal.MXID, err)
		}

		expectedLevel := 0
		if participant.JID == metadata.OwnerJID {
			expectedLevel = 95
		} else if participant.IsAdmin {
			expectedLevel = 50
		}
		changed = levels.EnsureUserLevel(puppet.MXID, expectedLevel) || changed
		if user != nil {
			changed = levels.EnsureUserLevel(user.MXID, expectedLevel) || changed
		}
	}
	if changed {
		_, err = portal.MainIntent().SetPowerLevels(portal.MXID, levels)
		if err != nil {
			portal.log.Errorln("Failed to change power levels:", err)
		}
	}
	portal.kickExtraUsers(participantMap)
}

func (portal *Portal) UpdateAvatar(user *User, updateInfo bool) bool {
	avatar, err := user.Client.GetProfilePictureInfo(portal.Key.JID, false)
	if err != nil {
		if !errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
			portal.log.Warnln("Failed to get avatar URL:", err)
		}
		return false
	} else if avatar == nil {
		if portal.Avatar == "remove" {
			return false
		}
		portal.AvatarURL = id.ContentURI{}
		avatar = &types.ProfilePictureInfo{ID: "remove"}
	} else if avatar.ID == portal.Avatar {
		return false
	} else if len(avatar.URL) == 0 {
		portal.log.Warnln("Didn't get URL in response to avatar query")
		return false
	} else {
		url, err := reuploadAvatar(portal.MainIntent(), avatar.URL)
		if err != nil {
			portal.log.Warnln("Failed to reupload avatar:", err)
			return false
		}
		portal.AvatarURL = url
	}

	if len(portal.MXID) > 0 {
		_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, portal.AvatarURL)
		if err != nil {
			portal.log.Warnln("Failed to set room topic:", err)
			return false
		}
	}
	portal.Avatar = avatar.ID
	if updateInfo {
		portal.UpdateBridgeInfo()
	}
	return true
}

func (portal *Portal) UpdateName(name string, setBy types.JID, intent *appservice.IntentAPI, updateInfo bool) bool {
	if name == "" && portal.IsBroadcastList() {
		name = UnnamedBroadcastName
	}
	if portal.Name != name {
		portal.log.Debugfln("Updating name %s -> %s", portal.Name, name)
		portal.Name = name
		if intent == nil {
			intent = portal.MainIntent()
			if !setBy.IsEmpty() {
				intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
			}
		}
		_, err := intent.SetRoomName(portal.MXID, name)
		if err == nil {
			if updateInfo {
				portal.UpdateBridgeInfo()
			}
			return true
		} else {
			portal.Name = ""
			portal.log.Warnln("Failed to set room name:", err)
		}
	}
	return false
}

func (portal *Portal) UpdateTopic(topic string, setBy types.JID, intent *appservice.IntentAPI, updateInfo bool) bool {
	if portal.Topic != topic {
		portal.log.Debugfln("Updating topic %s -> %s", portal.Topic, topic)
		portal.Topic = topic
		if intent == nil {
			intent = portal.MainIntent()
			if !setBy.IsEmpty() {
				intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
			}
		}
		_, err := intent.SetRoomTopic(portal.MXID, topic)
		if err == nil {
			if updateInfo {
				portal.UpdateBridgeInfo()
			}
			return true
		} else {
			portal.Topic = ""
			portal.log.Warnln("Failed to set room topic:", err)
		}
	}
	return false
}

func (portal *Portal) UpdateMetadata(user *User) bool {
	if portal.IsPrivateChat() {
		return false
	} else if portal.IsStatusBroadcastList() {
		update := false
		update = portal.UpdateName(StatusBroadcastName, types.EmptyJID, nil, false) || update
		update = portal.UpdateTopic(StatusBroadcastTopic, types.EmptyJID, nil, false) || update
		return update
	} else if portal.IsBroadcastList() {
		update := false
		//broadcastMetadata, err := user.Conn.GetBroadcastMetadata(portal.Key.JID)
		//if err == nil && broadcastMetadata.Status == 200 {
		//	portal.SyncBroadcastRecipients(user, broadcastMetadata)
		//	update = portal.UpdateName(broadcastMetadata.Name, "", nil, false) || update
		//} else {
		//	user.Conn.Store.ContactsLock.RLock()
		//	contact, _ := user.Conn.Store.Contacts[portal.Key.JID]
		//	user.Conn.Store.ContactsLock.RUnlock()
		//	update = portal.UpdateName(contact.Name, "", nil, false) || update
		//}
		//update = portal.UpdateTopic(BroadcastTopic, "", nil, false) || update
		return update
	}
	metadata, err := user.Client.GetGroupInfo(portal.Key.JID)
	if err != nil {
		portal.log.Errorln("Failed to get group info:", err)
		return false
	}

	portal.SyncParticipants(user, metadata)
	update := false
	update = portal.UpdateName(metadata.Name, metadata.NameSetBy, nil, false) || update
	update = portal.UpdateTopic(metadata.Topic, metadata.TopicSetBy, nil, false) || update

	portal.RestrictMessageSending(metadata.IsAnnounce)
	portal.RestrictMetadataChanges(metadata.IsLocked)

	return update
}

func (portal *Portal) userMXIDAction(user *User, fn func(mxid id.UserID)) {
	if user == nil {
		return
	}

	if user == portal.bridge.Relaybot {
		for _, mxid := range portal.bridge.Config.Bridge.Relaybot.InviteUsers {
			fn(mxid)
		}
	} else {
		fn(user.MXID)
	}
}

func (portal *Portal) ensureMXIDInvited(mxid id.UserID) {
	err := portal.MainIntent().EnsureInvited(portal.MXID, mxid)
	if err != nil {
		portal.log.Warnfln("Failed to ensure %s is invited to %s: %v", mxid, portal.MXID, err)
	}
}

func (portal *Portal) ensureUserInvited(user *User) {
	if user.IsRelaybot {
		portal.userMXIDAction(user, portal.ensureMXIDInvited)
		return
	}

	inviteContent := event.Content{
		Parsed: &event.MemberEventContent{
			Membership: event.MembershipInvite,
			IsDirect:   portal.IsPrivateChat(),
		},
		Raw: map[string]interface{}{},
	}
	customPuppet := portal.bridge.GetPuppetByCustomMXID(user.MXID)
	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		inviteContent.Raw["fi.mau.will_auto_accept"] = true
	}
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateMember, user.MXID.String(), &inviteContent)
	var httpErr mautrix.HTTPError
	if err != nil && errors.As(err, &httpErr) && httpErr.RespError != nil && strings.Contains(httpErr.RespError.Err, "is already in the room") {
		portal.bridge.StateStore.SetMembership(portal.MXID, user.MXID, event.MembershipJoin)
	} else if err != nil {
		portal.log.Warnfln("Failed to invite %s: %v", user.MXID, err)
	}

	if customPuppet != nil && customPuppet.CustomIntent() != nil {
		err = customPuppet.CustomIntent().EnsureJoined(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to auto-join portal as %s: %v", user.MXID, err)
		}
	}
}

func (portal *Portal) Sync(user *User) bool {
	portal.log.Infoln("Syncing portal for", user.MXID)

	if user.IsRelaybot {
		yes := true
		portal.hasRelaybot = &yes
	}

	if len(portal.MXID) == 0 {
		err := portal.CreateMatrixRoom(user)
		if err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return false
		}
	} else {
		portal.ensureUserInvited(user)
	}

	update := false
	update = portal.UpdateMetadata(user) || update
	if !portal.IsPrivateChat() && !portal.IsBroadcastList() && portal.Avatar == "" {
		update = portal.UpdateAvatar(user, false) || update
	}
	if update {
		portal.Update()
		portal.UpdateBridgeInfo()
	}
	return true
}

func (portal *Portal) GetBasePowerLevels() *event.PowerLevelsEventContent {
	anyone := 0
	nope := 99
	invite := 50
	if portal.bridge.Config.Bridge.AllowUserInvite {
		invite = 0
	}
	return &event.PowerLevelsEventContent{
		UsersDefault:    anyone,
		EventsDefault:   anyone,
		RedactPtr:       &anyone,
		StateDefaultPtr: &nope,
		BanPtr:          &nope,
		InvitePtr:       &invite,
		Users: map[id.UserID]int{
			portal.MainIntent().UserID: 100,
		},
		Events: map[string]int{
			event.StateRoomName.Type:   anyone,
			event.StateRoomAvatar.Type: anyone,
			event.StateTopic.Type:      anyone,
		},
	}
}

//func (portal *Portal) ChangeAdminStatus(jids []string, setAdmin bool) id.EventID {
//	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
//	if err != nil {
//		levels = portal.GetBasePowerLevels()
//	}
//	newLevel := 0
//	if setAdmin {
//		newLevel = 50
//	}
//	changed := false
//	for _, jid := range jids {
//		puppet := portal.bridge.GetPuppetByJID(jid)
//		changed = levels.EnsureUserLevel(puppet.MXID, newLevel) || changed
//
//		user := portal.bridge.GetUserByJID(jid)
//		if user != nil {
//			changed = levels.EnsureUserLevel(user.MXID, newLevel) || changed
//		}
//	}
//	if changed {
//		resp, err := portal.MainIntent().SetPowerLevels(portal.MXID, levels)
//		if err != nil {
//			portal.log.Errorln("Failed to change power levels:", err)
//		} else {
//			return resp.EventID
//		}
//	}
//	return ""
//}

func (portal *Portal) RestrictMessageSending(restrict bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}

	newLevel := 0
	if restrict {
		newLevel = 50
	}

	if levels.EventsDefault == newLevel {
		return ""
	}

	levels.EventsDefault = newLevel
	resp, err := portal.MainIntent().SetPowerLevels(portal.MXID, levels)
	if err != nil {
		portal.log.Errorln("Failed to change power levels:", err)
		return ""
	} else {
		return resp.EventID
	}
}

func (portal *Portal) RestrictMetadataChanges(restrict bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if restrict {
		newLevel = 50
	}
	changed := false
	changed = levels.EnsureEventLevel(event.StateRoomName, newLevel) || changed
	changed = levels.EnsureEventLevel(event.StateRoomAvatar, newLevel) || changed
	changed = levels.EnsureEventLevel(event.StateTopic, newLevel) || changed
	if changed {
		resp, err := portal.MainIntent().SetPowerLevels(portal.MXID, levels)
		if err != nil {
			portal.log.Errorln("Failed to change power levels:", err)
		} else {
			return resp.EventID
		}
	}
	return ""
}

func (portal *Portal) parseWebMessageInfo(webMsg *waProto.WebMessageInfo) *types.MessageInfo {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     portal.Key.JID,
			IsFromMe: webMsg.GetKey().GetFromMe(),
			IsGroup:  false,
		},
		ID:        webMsg.GetKey().GetId(),
		PushName:  webMsg.GetPushName(),
		Timestamp: time.Unix(int64(webMsg.GetMessageTimestamp()), 0),
	}
	var err error
	if info.IsFromMe {
		info.Sender = portal.Key.Receiver
	} else if portal.IsPrivateChat() {
		info.Sender = portal.Key.JID
	} else if webMsg.GetParticipant() != "" {
		info.Sender, err = types.ParseJID(webMsg.GetParticipant())
	} else if webMsg.GetKey().GetParticipant() != "" {
		info.Sender, err = types.ParseJID(webMsg.GetKey().GetParticipant())
	}
	if info.Sender.IsEmpty() {
		portal.log.Warnfln("Failed to get sender of message %s (parse error: %v)", info.ID, err)
		return nil
	}
	return &info
}

const backfillIDField = "net.maunium.whatsapp.id"

func (portal *Portal) wrapBatchEvent(info *types.MessageInfo, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent) (*event.Event, error) {
	wrappedContent := event.Content{
		Parsed: content,
		Raw: map[string]interface{}{
			backfillIDField: info.ID,
		},
	}
	if intent.IsCustomPuppet {
		wrappedContent.Raw["net.maunium.whatsapp.puppet"] = intent.IsCustomPuppet
	}
	newEventType, err := portal.encrypt(&wrappedContent, eventType)
	if err != nil {
		return nil, err
	}
	return &event.Event{
		Sender:    intent.UserID,
		Type:      newEventType,
		Timestamp: info.Timestamp.UnixMilli(),
		Content:   wrappedContent,
	}, nil
}

func (portal *Portal) appendBatchEvents(converted *ConvertedMessage, info *types.MessageInfo, eventsArray *[]*event.Event, infoArray *[]*types.MessageInfo) error {
	mainEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Content)
	if err != nil {
		return err
	}
	if converted.Caption != nil {
		captionEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Caption)
		if err != nil {
			return err
		}
		*eventsArray = append(*eventsArray, mainEvt, captionEvt)
		*infoArray = append(*infoArray, nil, info)
	} else {
		*eventsArray = append(*eventsArray, mainEvt)
		*infoArray = append(*infoArray, info)
	}
	return nil
}

func (portal *Portal) finishBatch(eventIDs []id.EventID, infos []*types.MessageInfo) {
	if len(eventIDs) != len(infos) {
		portal.log.Errorfln("Length of event IDs (%d) and message infos (%d) doesn't match! Using slow path for mapping event IDs", len(eventIDs), len(infos))
		infoMap := make(map[types.MessageID]*types.MessageInfo, len(infos))
		for _, info := range infos {
			infoMap[info.ID] = info
		}
		for _, eventID := range eventIDs {
			if evt, err := portal.MainIntent().GetEvent(portal.MXID, eventID); err != nil {
				portal.log.Warnfln("Failed to get event %s to register it in the database: %v", eventID, err)
			} else if msgID, ok := evt.Content.Raw[backfillIDField].(string); !ok {
				portal.log.Warnfln("Event %s doesn't include the WhatsApp message ID", eventID)
			} else if info, ok := infoMap[types.MessageID(msgID)]; !ok {
				portal.log.Warnfln("Didn't find info of message %s (event %s) to register it in the database", msgID, eventID)
			} else {
				portal.markHandled(nil, info, eventID, true, false, false)
			}
		}
	} else {
		for i := 0; i < len(infos); i++ {
			if infos[i] != nil {
				portal.markHandled(nil, infos[i], eventIDs[i], true, false, false)
			}
		}
		portal.log.Infofln("Successfully sent %d events", len(eventIDs))
	}
}

func (portal *Portal) backfill(source *User, messages []*waProto.HistorySyncMsg) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()

	var historyBatch, newBatch mautrix.ReqBatchSend
	var historyBatchInfos, newBatchInfos []*types.MessageInfo

	firstMsgTimestamp := time.Unix(int64(messages[len(messages)-1].GetMessage().GetMessageTimestamp()), 0)

	historyBatch.StateEventsAtStart = make([]*event.Event, 1)
	newBatch.StateEventsAtStart = make([]*event.Event, 1)

	// TODO remove the dummy state events after https://github.com/matrix-org/synapse/pull/11188
	emptyStr := ""
	dummyStateEvent := event.Event{
		Type:      BackfillDummyStateEvent,
		Sender:    portal.MainIntent().UserID,
		StateKey:  &emptyStr,
		Timestamp: firstMsgTimestamp.UnixMilli(),
		Content:   event.Content{},
	}
	historyBatch.StateEventsAtStart[0] = &dummyStateEvent
	newBatch.StateEventsAtStart[0] = &dummyStateEvent

	addedMembers := make(map[id.UserID]*event.MemberEventContent)
	addMember := func(puppet *Puppet) {
		if _, alreadyAdded := addedMembers[puppet.MXID]; alreadyAdded {
			return
		}
		mxid := puppet.MXID.String()
		content := event.MemberEventContent{
			Membership:  event.MembershipJoin,
			Displayname: puppet.Displayname,
			AvatarURL:   puppet.AvatarURL.CUString(),
		}
		inviteContent := content
		inviteContent.Membership = event.MembershipInvite
		historyBatch.StateEventsAtStart = append(historyBatch.StateEventsAtStart, &event.Event{
			Type:      event.StateMember,
			Sender:    portal.MainIntent().UserID,
			StateKey:  &mxid,
			Timestamp: firstMsgTimestamp.UnixMilli(),
			Content:   event.Content{Parsed: &inviteContent},
		}, &event.Event{
			Type:      event.StateMember,
			Sender:    puppet.MXID,
			StateKey:  &mxid,
			Timestamp: firstMsgTimestamp.UnixMilli(),
			Content:   event.Content{Parsed: &content},
		})
		addedMembers[puppet.MXID] = &content
	}

	firstMessage := portal.bridge.DB.Message.GetFirstInChat(portal.Key)
	lastMessage := portal.bridge.DB.Message.GetLastInChat(portal.Key)
	var historyMaxTs, newMinTs time.Time

	if portal.FirstEventID != "" || portal.NextBatchID != "" {
		historyBatch.PrevEventID = portal.FirstEventID
		historyBatch.BatchID = portal.NextBatchID
		if firstMessage == nil && lastMessage == nil {
			historyMaxTs = time.Now()
		} else {
			historyMaxTs = firstMessage.Timestamp
		}
	}
	if lastMessage != nil {
		newBatch.PrevEventID = lastMessage.MXID
		newMinTs = lastMessage.Timestamp
	}

	portal.log.Infofln("Processing history sync with %d messages", len(messages))
	// The messages are ordered newest to oldest, so iterate them in reverse order.
	for i := len(messages) - 1; i >= 0; i-- {
		wrappedMsg := messages[i]
		webMsg := wrappedMsg.GetMessage()
		msgType := portal.getMessageType(webMsg.GetMessage())
		if msgType == "unknown" || msgType == "ignore" {
			if msgType == "unknown" {
				portal.log.Debugfln("Skipping message %s with unknown type in backfill", webMsg.GetKey().GetId())
			}
			continue
		}
		info := portal.parseWebMessageInfo(webMsg)
		if info == nil {
			continue
		}
		var batch *mautrix.ReqBatchSend
		var infos *[]*types.MessageInfo
		var history bool
		if !historyMaxTs.IsZero() && info.Timestamp.Before(historyMaxTs) {
			batch, infos, history = &historyBatch, &historyBatchInfos, true
		} else if !newMinTs.IsZero() && info.Timestamp.After(newMinTs) {
			batch, infos = &newBatch, &newBatchInfos
		} else {
			continue
		}
		puppet := portal.getMessagePuppet(source, info)
		var intent *appservice.IntentAPI
		if portal.Key.JID == puppet.JID {
			intent = puppet.DefaultIntent()
		} else {
			intent = puppet.IntentFor(portal)
			if intent.IsCustomPuppet && !portal.bridge.Config.Bridge.DoublePuppetBackfill {
				intent = puppet.DefaultIntent()
				addMember(puppet)
			}
		}
		converted := portal.convertMessage(intent, source, info, webMsg.GetMessage())
		if converted == nil {
			portal.log.Debugfln("Skipping unsupported message %s in backfill", info.ID)
			continue
		}
		if history && !portal.IsPrivateChat() && !portal.bridge.StateStore.IsInRoom(portal.MXID, puppet.MXID) {
			addMember(puppet)
		}
		err := portal.appendBatchEvents(converted, info, &batch.Events, infos)
		if err != nil {
			portal.log.Errorfln("Error handling message %s during backfill: %v", info.ID, err)
		}
	}

	if len(historyBatch.Events) > 0 && len(historyBatch.PrevEventID) > 0 {
		portal.log.Infofln("Sending %d historical messages...", len(historyBatch.Events))
		historyResp, err := portal.MainIntent().BatchSend(portal.MXID, &historyBatch)
		if err != nil {
			portal.log.Errorln("Error sending batch of historical messages:", err)
		} else {
			portal.finishBatch(historyResp.EventIDs, historyBatchInfos)
			portal.NextBatchID = historyResp.NextBatchID
			portal.Update()
			// If batchID is non-empty, it means this is backfilling very old messages, and we don't need a post-backfill dummy.
			if historyBatch.BatchID == "" {
				portal.sendPostBackfillDummy(time.UnixMilli(historyBatch.Events[len(historyBatch.Events)-1].Timestamp))
			}
		}
	}

	if len(newBatch.Events) > 0 && len(newBatch.PrevEventID) > 0 {
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with forward backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, ForwardBackfillDummyEvent, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending dummy event for forward backfill:", err)
		}
		portal.log.Infofln("Sending %d new messages...", len(newBatch.Events))
		newResp, err := portal.MainIntent().BatchSend(portal.MXID, &newBatch)
		if err != nil {
			portal.log.Errorln("Error sending batch of new messages:", err)
		} else {
			portal.finishBatch(newResp.EventIDs, newBatchInfos)
			portal.sendPostBackfillDummy(time.UnixMilli(newBatch.Events[len(newBatch.Events)-1].Timestamp))
		}
	}
}

func (portal *Portal) sendPostBackfillDummy(lastTimestamp time.Time) {
	resp, err := portal.MainIntent().SendMessageEvent(portal.MXID, BackfillEndDummyEvent, struct{}{})
	if err != nil {
		portal.log.Errorln("Error sending post-backfill dummy event:", err)
		return
	}
	msg := portal.bridge.DB.Message.New()
	msg.Chat = portal.Key
	msg.MXID = resp.EventID
	msg.JID = types.MessageID(resp.EventID)
	msg.Timestamp = lastTimestamp.Add(1 * time.Second)
	msg.Sent = true
	msg.Insert()
}

type BridgeInfoSection struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"displayname,omitempty"`
	AvatarURL   id.ContentURIString `json:"avatar_url,omitempty"`
	ExternalURL string              `json:"external_url,omitempty"`
}

type BridgeInfoContent struct {
	BridgeBot id.UserID          `json:"bridgebot"`
	Creator   id.UserID          `json:"creator,omitempty"`
	Protocol  BridgeInfoSection  `json:"protocol"`
	Network   *BridgeInfoSection `json:"network,omitempty"`
	Channel   BridgeInfoSection  `json:"channel"`
}

var (
	StateBridgeInfo         = event.Type{Type: "m.bridge", Class: event.StateEventType}
	StateHalfShotBridgeInfo = event.Type{Type: "uk.half-shot.bridge", Class: event.StateEventType}
)

func (portal *Portal) getBridgeInfo() (string, BridgeInfoContent) {
	bridgeInfo := BridgeInfoContent{
		BridgeBot: portal.bridge.Bot.UserID,
		Creator:   portal.MainIntent().UserID,
		Protocol: BridgeInfoSection{
			ID:          "whatsapp",
			DisplayName: "WhatsApp",
			AvatarURL:   id.ContentURIString(portal.bridge.Config.AppService.Bot.Avatar),
			ExternalURL: "https://www.whatsapp.com/",
		},
		Channel: BridgeInfoSection{
			ID:          portal.Key.JID.String(),
			DisplayName: portal.Name,
			AvatarURL:   portal.AvatarURL.CUString(),
		},
	}
	bridgeInfoStateKey := fmt.Sprintf("net.maunium.whatsapp://whatsapp/%s", portal.Key.JID)
	return bridgeInfoStateKey, bridgeInfo
}

func (portal *Portal) UpdateBridgeInfo() {
	if len(portal.MXID) == 0 {
		portal.log.Debugln("Not updating bridge info: no Matrix room created")
		return
	}
	portal.log.Debugln("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, StateBridgeInfo, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, StateHalfShotBridgeInfo, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

var PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
var BackfillDummyStateEvent = event.Type{Type: "fi.mau.dummy.blank_backfill_state", Class: event.StateEventType}
var BackfillEndDummyEvent = event.Type{Type: "fi.mau.dummy.backfill_end", Class: event.MessageEventType}
var ForwardBackfillDummyEvent = event.Type{Type: "fi.mau.dummy.pre_forward_backfill", Class: event.MessageEventType}

func (portal *Portal) CreateMatrixRoom(user *User) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if len(portal.MXID) > 0 {
		return nil
	}

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(); err != nil {
		return err
	}

	portal.log.Infoln("Creating Matrix room. Info source:", user.MXID)

	var metadata *types.GroupInfo
	//var broadcastMetadata *types.BroadcastListInfo
	if portal.IsPrivateChat() {
		puppet := portal.bridge.GetPuppetByJID(portal.Key.JID)
		puppet.SyncContact(user, true)
		if portal.bridge.Config.Bridge.PrivateChatPortalMeta {
			portal.Name = puppet.Displayname
			portal.AvatarURL = puppet.AvatarURL
			portal.Avatar = puppet.Avatar
		} else {
			portal.Name = ""
		}
		portal.Topic = PrivateChatTopic
	} else if portal.IsStatusBroadcastList() {
		if !portal.bridge.Config.Bridge.EnableStatusBroadcast {
			portal.log.Debugln("Status bridging is disabled in config, not creating room after all")
			return ErrStatusBroadcastDisabled
		}
		portal.Name = StatusBroadcastName
		portal.Topic = StatusBroadcastTopic
	} else if portal.IsBroadcastList() {
		//var err error
		//broadcastMetadata, err = user.Conn.GetBroadcastMetadata(portal.Key.JID)
		//if err == nil && broadcastMetadata.Status == 200 {
		//	portal.Name = broadcastMetadata.Name
		//} else {
		//	user.Conn.Store.ContactsLock.RLock()
		//	contact, _ := user.Conn.Store.Contacts[portal.Key.JID]
		//	user.Conn.Store.ContactsLock.RUnlock()
		//	portal.Name = contact.Name
		//}
		//if len(portal.Name) == 0 {
		//	portal.Name = UnnamedBroadcastName
		//}
		//portal.Topic = BroadcastTopic
		portal.log.Debugln("Broadcast list is not yet supported, not creating room after all")
		return fmt.Errorf("broadcast list bridging is currently not supported")
	} else {
		var err error
		metadata, err = user.Client.GetGroupInfo(portal.Key.JID)
		if err == nil {
			portal.Name = metadata.Name
			portal.Topic = metadata.Topic
		}
		portal.UpdateAvatar(user, false)
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()

	initialState := []*event.Event{{
		Type: event.StatePowerLevels,
		Content: event.Content{
			Parsed: portal.GetBasePowerLevels(),
		},
	}, {
		Type:     StateBridgeInfo,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     StateHalfShotBridgeInfo,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}}
	if !portal.AvatarURL.IsEmpty() {
		initialState = append(initialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{
				Parsed: event.RoomAvatarEventContent{URL: portal.AvatarURL},
			},
		})
	}

	var invite []id.UserID
	if user.IsRelaybot {
		invite = portal.bridge.Config.Bridge.Relaybot.InviteUsers
	}

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1},
			},
		})
		portal.Encrypted = true
		if portal.IsPrivateChat() {
			invite = append(invite, portal.bridge.Bot.UserID)
		}
	}

	resp, err := intent.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:   "private",
		Name:         portal.Name,
		Topic:        portal.Topic,
		Invite:       invite,
		Preset:       "private_chat",
		IsDirect:     portal.IsPrivateChat(),
		InitialState: initialState,
	})
	if err != nil {
		return err
	}
	portal.MXID = resp.RoomID
	portal.Update()
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()

	// We set the memberships beforehand to make sure the encryption key exchange in initial backfill knows the users are here.
	for _, userID := range invite {
		portal.bridge.StateStore.SetMembership(portal.MXID, userID, event.MembershipInvite)
	}

	portal.ensureUserInvited(user)

	if metadata != nil {
		portal.SyncParticipants(user, metadata)
		if metadata.IsAnnounce {
			portal.RestrictMessageSending(metadata.IsAnnounce)
		}
		if metadata.IsLocked {
			portal.RestrictMetadataChanges(metadata.IsLocked)
		}
	}
	//if broadcastMetadata != nil {
	//	portal.SyncBroadcastRecipients(user, broadcastMetadata)
	//}
	if portal.IsPrivateChat() && !user.IsRelaybot {
		puppet := user.bridge.GetPuppetByJID(portal.Key.JID)

		if portal.bridge.Config.Bridge.Encryption.Default {
			err = portal.bridge.Bot.EnsureJoined(portal.MXID)
			if err != nil {
				portal.log.Errorln("Failed to join created portal with bridge bot for e2be:", err)
			}
		}

		user.UpdateDirectChats(map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}})
	}

	firstEventResp, err := portal.MainIntent().SendMessageEvent(portal.MXID, PortalCreationDummyEvent, struct{}{})
	if err != nil {
		portal.log.Errorln("Failed to send dummy event to mark portal creation:", err)
	} else {
		portal.FirstEventID = firstEventResp.EventID
		portal.Update()
	}

	//user.CreateUserPortal(database.PortalKeyWithMeta{PortalKey: portal.Key, InCommunity: inCommunity})

	//err = portal.FillInitialHistory(user)
	//if err != nil {
	//	portal.log.Errorln("Failed to fill history:", err)
	//}
	return nil
}

func (portal *Portal) IsPrivateChat() bool {
	return portal.Key.JID.Server == types.DefaultUserServer
}

func (portal *Portal) IsBroadcastList() bool {
	return portal.Key.JID.Server == types.BroadcastServer
}

func (portal *Portal) IsStatusBroadcastList() bool {
	return portal.Key.JID == types.StatusBroadcastJID
}

func (portal *Portal) HasRelaybot() bool {
	if portal.bridge.Relaybot == nil {
		return false
	} else if portal.hasRelaybot == nil {
		// FIXME
		//val := portal.bridge.Relaybot.IsInPortal(portal.Key)
		val := true
		portal.hasRelaybot = &val
	}
	return *portal.hasRelaybot
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.bridge.GetPuppetByJID(portal.Key.JID).DefaultIntent()
	}
	return portal.bridge.Bot
}

func (portal *Portal) SetReply(content *event.MessageEventContent, replyToID types.MessageID) {
	if len(replyToID) == 0 {
		return
	}
	message := portal.bridge.DB.Message.GetByJID(portal.Key, replyToID)
	if message != nil && !message.IsFakeMXID() {
		evt, err := portal.MainIntent().GetEvent(portal.MXID, message.MXID)
		if err != nil {
			portal.log.Warnln("Failed to get reply target:", err)
			return
		}
		if evt.Type == event.EventEncrypted {
			_ = evt.Content.ParseRaw(evt.Type)
			decryptedEvt, err := portal.bridge.Crypto.Decrypt(evt)
			if err != nil {
				portal.log.Warnln("Failed to decrypt reply target:", err)
			} else {
				evt = decryptedEvt
			}
		}
		_ = evt.Content.ParseRaw(evt.Type)
		content.SetReply(evt)
	}
	return
}

func (portal *Portal) HandleMessageRevoke(user *User, info *types.MessageInfo, key *waProto.MessageKey) bool {
	msg := portal.bridge.DB.Message.GetByJID(portal.Key, key.GetId())
	if msg == nil || msg.IsFakeMXID() {
		return false
	}
	intent := portal.bridge.GetPuppetByJID(info.Sender).IntentFor(portal)
	_, err := intent.RedactEvent(portal.MXID, msg.MXID)
	if err != nil {
		if errors.Is(err, mautrix.MForbidden) {
			_, err = portal.MainIntent().RedactEvent(portal.MXID, msg.MXID)
			if err != nil {
				portal.log.Errorln("Failed to redact %s: %v", msg.JID, err)
			}
		}
	} else {
		msg.Delete()
	}
	return true
}

//func (portal *Portal) HandleFakeMessage(_ *User, message FakeMessage) bool {
//	if portal.isRecentlyHandled(message.ID) {
//		return false
//	}
//
//	content := event.MessageEventContent{
//		MsgType: event.MsgNotice,
//		Body:    message.Text,
//	}
//	if message.Alert {
//		content.MsgType = event.MsgText
//	}
//	_, err := portal.sendMainIntentMessage(content)
//	if err != nil {
//		portal.log.Errorfln("Failed to handle fake message %s: %v", message.ID, err)
//		return true
//	}
//
//	portal.recentlyHandledLock.Lock()
//	index := portal.recentlyHandledIndex
//	portal.recentlyHandledIndex = (portal.recentlyHandledIndex + 1) % recentlyHandledLength
//	portal.recentlyHandledLock.Unlock()
//	portal.recentlyHandled[index] = message.ID
//	return true
//}

func (portal *Portal) sendMainIntentMessage(content interface{}) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(portal.MainIntent(), event.EventMessage, content, 0)
}

func (portal *Portal) encrypt(content *event.Content, eventType event.Type) (event.Type, error) {
	if portal.Encrypted && portal.bridge.Crypto != nil {
		// TODO maybe the locking should be inside mautrix-go?
		portal.encryptLock.Lock()
		encrypted, err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, *content)
		portal.encryptLock.Unlock()
		if err != nil {
			return eventType, fmt.Errorf("failed to encrypt event: %w", err)
		}
		eventType = event.EventEncrypted
		content.Parsed = encrypted
	}
	return eventType, nil
}

func (portal *Portal) sendMessage(intent *appservice.IntentAPI, eventType event.Type, content interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content}
	if timestamp != 0 && intent.IsCustomPuppet {
		wrappedContent.Raw = map[string]interface{}{
			"net.maunium.whatsapp.puppet": intent.IsCustomPuppet,
		}
	}
	var err error
	eventType, err = portal.encrypt(&wrappedContent, eventType)
	if err != nil {
		return nil, err
	}
	_, _ = intent.UserTyping(portal.MXID, false, 0)
	if timestamp == 0 {
		return intent.SendMessageEvent(portal.MXID, eventType, &wrappedContent)
	} else {
		return intent.SendMassagedMessageEvent(portal.MXID, eventType, &wrappedContent, timestamp)
	}
}

type ConvertedMessage struct {
	Intent  *appservice.IntentAPI
	Type    event.Type
	Content *event.MessageEventContent
	Caption *event.MessageEventContent
}

func (portal *Portal) convertTextMessage(intent *appservice.IntentAPI, msg *waProto.Message) *ConvertedMessage {
	content := &event.MessageEventContent{
		Body:    msg.GetConversation(),
		MsgType: event.MsgText,
	}
	if msg.GetExtendedTextMessage() != nil {
		content.Body = msg.GetExtendedTextMessage().GetText()

		contextInfo := msg.GetExtendedTextMessage().GetContextInfo()
		if contextInfo != nil {
			portal.bridge.Formatter.ParseWhatsApp(content, contextInfo.GetMentionedJid())
			portal.SetReply(content, contextInfo.GetStanzaId())
		}
	}

	return &ConvertedMessage{Intent: intent, Type: event.EventMessage, Content: content}
}

//func (portal *Portal) HandleStubMessage(source *User, message whatsapp.StubMessage, isBackfill bool) bool {
//	if portal.bridge.Config.Bridge.ChatMetaSync && (!portal.IsBroadcastList() || isBackfill) {
//		// Chat meta sync is enabled, so we use chat update commands and full-syncs instead of message history
//		// However, broadcast lists don't have update commands, so we handle these if it's not a backfill
//		return false
//	}
//	intent := portal.startHandling(source, message.Info, fmt.Sprintf("stub %s", message.Type.String()))
//	if intent == nil {
//		return false
//	}
//	var senderJID string
//	if message.Info.FromMe {
//		senderJID = source.JID
//	} else {
//		senderJID = message.Info.SenderJid
//	}
//	var eventID id.EventID
//	// TODO find more real event IDs
//	// TODO timestamp massaging
//	switch message.Type {
//	case waProto.WebMessageInfo_GROUP_CHANGE_SUBJECT:
//		portal.UpdateName(message.FirstParam, "", intent, true)
//	case waProto.WebMessageInfo_GROUP_CHANGE_ICON:
//		portal.UpdateAvatar(source, nil, true)
//	case waProto.WebMessageInfo_GROUP_CHANGE_DESCRIPTION:
//		if isBackfill {
//			// TODO fetch topic from server
//		}
//		//portal.UpdateTopic(message.FirstParam, "", intent, true)
//	case waProto.WebMessageInfo_GROUP_CHANGE_ANNOUNCE:
//		eventID = portal.RestrictMessageSending(message.FirstParam == "on")
//	case waProto.WebMessageInfo_GROUP_CHANGE_RESTRICT:
//		eventID = portal.RestrictMetadataChanges(message.FirstParam == "on")
//	case waProto.WebMessageInfo_GROUP_PARTICIPANT_ADD, waProto.WebMessageInfo_GROUP_PARTICIPANT_INVITE, waProto.WebMessageInfo_BROADCAST_ADD:
//		eventID = portal.HandleWhatsAppInvite(source, senderJID, intent, message.Params)
//	case waProto.WebMessageInfo_GROUP_PARTICIPANT_REMOVE, waProto.WebMessageInfo_GROUP_PARTICIPANT_LEAVE, waProto.WebMessageInfo_BROADCAST_REMOVE:
//		portal.HandleWhatsAppKick(source, senderJID, message.Params)
//	case waProto.WebMessageInfo_GROUP_PARTICIPANT_PROMOTE:
//		eventID = portal.ChangeAdminStatus(message.Params, true)
//	case waProto.WebMessageInfo_GROUP_PARTICIPANT_DEMOTE:
//		eventID = portal.ChangeAdminStatus(message.Params, false)
//	default:
//		return false
//	}
//	if len(eventID) == 0 {
//		eventID = id.EventID(fmt.Sprintf("net.maunium.whatsapp.fake::%s", message.Info.Id))
//	}
//	portal.markHandled(source, message.Info.Source, eventID, true)
//	return true
//}

func (portal *Portal) convertLocationMessage(intent *appservice.IntentAPI, msg *waProto.LocationMessage) *ConvertedMessage {
	url := msg.GetUrl()
	if len(url) == 0 {
		url = fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude())
	}
	name := msg.GetName()
	if len(name) == 0 {
		latChar := 'N'
		if msg.GetDegreesLatitude() < 0 {
			latChar = 'S'
		}
		longChar := 'E'
		if msg.GetDegreesLongitude() < 0 {
			longChar = 'W'
		}
		name = fmt.Sprintf("%.4f° %c %.4f° %c", math.Abs(msg.GetDegreesLatitude()), latChar, math.Abs(msg.GetDegreesLongitude()), longChar)
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgLocation,
		Body:          fmt.Sprintf("Location: %s\n%s\n%s", name, msg.GetAddress(), url),
		Format:        event.FormatHTML,
		FormattedBody: fmt.Sprintf("Location: <a href='%s'>%s</a><br>%s", url, name, msg.GetAddress()),
		GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", msg.GetDegreesLatitude(), msg.GetDegreesLongitude()),
	}

	if len(msg.GetJpegThumbnail()) > 0 {
		thumbnailMime := http.DetectContentType(msg.GetJpegThumbnail())
		uploadedThumbnail, _ := intent.UploadBytes(msg.GetJpegThumbnail(), thumbnailMime)
		if uploadedThumbnail != nil {
			cfg, _, _ := image.DecodeConfig(bytes.NewReader(msg.GetJpegThumbnail()))
			content.Info = &event.FileInfo{
				ThumbnailInfo: &event.FileInfo{
					Size:     len(msg.GetJpegThumbnail()),
					Width:    cfg.Width,
					Height:   cfg.Height,
					MimeType: thumbnailMime,
				},
				ThumbnailURL: uploadedThumbnail.ContentURI.CUString(),
			}
		}
	}

	portal.SetReply(content, msg.GetContextInfo().GetStanzaId())

	return &ConvertedMessage{Intent: intent, Type: event.EventMessage, Content: content}
}

func (portal *Portal) convertContactMessage(intent *appservice.IntentAPI, msg *waProto.ContactMessage) *ConvertedMessage {
	fileName := fmt.Sprintf("%s.vcf", msg.GetDisplayName())
	data := []byte(msg.GetVcard())
	mimeType := "text/vcard"
	data, uploadMimeType, file := portal.encryptFile(data, mimeType)

	uploadResp, err := intent.UploadBytesWithName(data, uploadMimeType, fileName)
	if err != nil {
		portal.log.Errorfln("Failed to upload vcard of %s: %v", msg.GetDisplayName(), err)
		return nil
	}

	content := &event.MessageEventContent{
		Body:    fileName,
		MsgType: event.MsgFile,
		File:    file,
		Info: &event.FileInfo{
			MimeType: mimeType,
			Size:     len(msg.GetVcard()),
		},
	}
	if content.File != nil {
		content.File.URL = uploadResp.ContentURI.CUString()
	} else {
		content.URL = uploadResp.ContentURI.CUString()
	}

	portal.SetReply(content, msg.GetContextInfo().GetStanzaId())

	return &ConvertedMessage{Intent: intent, Type: event.EventMessage, Content: content}
}

// FIXME
//func (portal *Portal) tryKickUser(userID id.UserID, intent *appservice.IntentAPI) error {
//	_, err := intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
//	if err != nil {
//		httpErr, ok := err.(mautrix.HTTPError)
//		if ok && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_FORBIDDEN" {
//			_, err = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
//		}
//	}
//	return err
//}
//
//func (portal *Portal) removeUser(isSameUser bool, kicker *appservice.IntentAPI, target id.UserID, targetIntent *appservice.IntentAPI) {
//	if !isSameUser || targetIntent == nil {
//		err := portal.tryKickUser(target, kicker)
//		if err != nil {
//			portal.log.Warnfln("Failed to kick %s from %s: %v", target, portal.MXID, err)
//			if targetIntent != nil {
//				_, _ = targetIntent.LeaveRoom(portal.MXID)
//			}
//		}
//	} else {
//		_, err := targetIntent.LeaveRoom(portal.MXID)
//		if err != nil {
//			portal.log.Warnfln("Failed to leave portal as %s: %v", target, err)
//			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: target})
//		}
//	}
//}
//
//func (portal *Portal) HandleWhatsAppKick(source *User, senderJID string, jids []string) {
//	sender := portal.bridge.GetPuppetByJID(senderJID)
//	senderIntent := sender.IntentFor(portal)
//	for _, jid := range jids {
//		if source != nil && source.JID == jid {
//			portal.log.Debugln("Ignoring self-kick by", source.MXID)
//			continue
//		}
//		puppet := portal.bridge.GetPuppetByJID(jid)
//		portal.removeUser(puppet.JID == sender.JID, senderIntent, puppet.MXID, puppet.DefaultIntent())
//
//		if !portal.IsBroadcastList() {
//			user := portal.bridge.GetUserByJID(jid)
//			if user != nil {
//				var customIntent *appservice.IntentAPI
//				if puppet.CustomMXID == user.MXID {
//					customIntent = puppet.CustomIntent()
//				}
//				portal.removeUser(puppet.JID == sender.JID, senderIntent, user.MXID, customIntent)
//			}
//		}
//	}
//}
//
//func (portal *Portal) HandleWhatsAppInvite(source *User, senderJID string, intent *appservice.IntentAPI, jids []string) (evtID id.EventID) {
//	if intent == nil {
//		intent = portal.MainIntent()
//		if senderJID != "unknown" {
//			sender := portal.bridge.GetPuppetByJID(senderJID)
//			intent = sender.IntentFor(portal)
//		}
//	}
//	for _, jid := range jids {
//		puppet := portal.bridge.GetPuppetByJID(jid)
//		puppet.SyncContact(source, true)
//		content := event.Content{
//			Parsed: event.MemberEventContent{
//				Membership:  "invite",
//				Displayname: puppet.Displayname,
//				AvatarURL:   puppet.AvatarURL.CUString(),
//			},
//			Raw: map[string]interface{}{
//				"net.maunium.whatsapp.puppet": true,
//			},
//		}
//		resp, err := intent.SendStateEvent(portal.MXID, event.StateMember, puppet.MXID.String(), &content)
//		if err != nil {
//			portal.log.Warnfln("Failed to invite %s as %s: %v", puppet.MXID, intent.UserID, err)
//			_ = portal.MainIntent().EnsureInvited(portal.MXID, puppet.MXID)
//		} else {
//			evtID = resp.EventID
//		}
//		err = puppet.DefaultIntent().EnsureJoined(portal.MXID)
//		if err != nil {
//			portal.log.Errorfln("Failed to ensure %s is joined: %v", puppet.MXID, err)
//		}
//	}
//	return
//}

func (portal *Portal) makeMediaBridgeFailureMessage(intent *appservice.IntentAPI, info *types.MessageInfo, bridgeErr error, captionContent *event.MessageEventContent) *ConvertedMessage {
	portal.log.Errorfln("Failed to bridge media for %s: %v", info.ID, bridgeErr)
	return &ConvertedMessage{Intent: intent, Type: event.EventMessage, Content: &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    "Failed to bridge media",
	}, Caption: captionContent}
}

func (portal *Portal) encryptFile(data []byte, mimeType string) ([]byte, string, *event.EncryptedFileInfo) {
	if !portal.Encrypted {
		return data, mimeType, nil
	}

	file := &event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	return file.Encrypt(data), "application/octet-stream", file
}

type MediaMessage interface {
	whatsmeow.DownloadableMessage
	GetContextInfo() *waProto.ContextInfo
	GetMimetype() string
}

type MediaMessageWithThumbnail interface {
	MediaMessage
	GetJpegThumbnail() []byte
	GetCaption() string
}

type MediaMessageWithCaption interface {
	MediaMessage
	GetCaption() string
}

type MediaMessageWithFileName interface {
	MediaMessage
	GetFileName() string
}

type MediaMessageWithDuration interface {
	MediaMessage
	GetSeconds() uint32
}

func (portal *Portal) convertMediaMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, msg MediaMessage) *ConvertedMessage {
	messageWithCaption, ok := msg.(MediaMessageWithCaption)
	var captionContent *event.MessageEventContent
	if ok && len(messageWithCaption.GetCaption()) > 0 {
		captionContent = &event.MessageEventContent{
			Body:    messageWithCaption.GetCaption(),
			MsgType: event.MsgNotice,
		}

		portal.bridge.Formatter.ParseWhatsApp(captionContent, msg.GetContextInfo().GetMentionedJid())
	}

	data, err := source.Client.Download(msg)
	// TODO can these errors still be handled?
	//if errors.Is(err, whatsapp.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsapp.ErrMediaDownloadFailedWith410) {
	//	portal.log.Warnfln("Failed to download media for %s: %v. Calling LoadMediaInfo and retrying download...", msg.info.Id, err)
	//	_, err = source.Conn.LoadMediaInfo(msg.info.RemoteJid, msg.info.Id, msg.info.FromMe)
	//	if err != nil {
	//		portal.sendMediaBridgeFailure(source, intent, msg.info, fmt.Errorf("failed to load media info: %w", err))
	//		return true
	//	}
	//	data, err = msg.download()
	//}
	if errors.Is(err, whatsmeow.ErrNoURLPresent) {
		portal.log.Debugfln("No URL present error for media message %s, ignoring...", info.ID)
		return nil
	} else if err != nil {
		return portal.makeMediaBridgeFailureMessage(intent, info, err, captionContent)
	}

	var width, height int
	if strings.HasPrefix(msg.GetMimetype(), "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		width, height = cfg.Width, cfg.Height
	}

	data, uploadMimeType, file := portal.encryptFile(data, msg.GetMimetype())

	uploaded, err := intent.UploadBytes(data, uploadMimeType)
	if err != nil {
		if errors.Is(err, mautrix.MTooLarge) {
			return portal.makeMediaBridgeFailureMessage(intent, info, errors.New("homeserver rejected too large file"), captionContent)
		} else if httpErr, ok := err.(mautrix.HTTPError); ok && httpErr.IsStatus(413) {
			return portal.makeMediaBridgeFailureMessage(intent, info, errors.New("proxy rejected too large file"), captionContent)
		} else {
			return portal.makeMediaBridgeFailureMessage(intent, info, fmt.Errorf("failed to upload media: %w", err), captionContent)
		}
	}

	content := &event.MessageEventContent{
		File: file,
		Info: &event.FileInfo{
			Size:     len(data),
			MimeType: msg.GetMimetype(),
			Width:    width,
			Height:   height,
		},
	}

	msgWithName, ok := msg.(MediaMessageWithFileName)
	if ok && len(msgWithName.GetFileName()) > 0 {
		content.Body = msgWithName.GetFileName()
	} else {
		mimeClass := strings.Split(msg.GetMimetype(), "/")[0]
		switch mimeClass {
		case "application":
			content.Body = "file"
		default:
			content.Body = mimeClass
		}

		exts, _ := mime.ExtensionsByType(msg.GetMimetype())
		if exts != nil && len(exts) > 0 {
			content.Body += exts[0]
		}
	}

	msgWithDuration, ok := msg.(MediaMessageWithDuration)
	if ok {
		content.Info.Duration = int(msgWithDuration.GetSeconds()) * 1000
	}

	if content.File != nil {
		content.File.URL = uploaded.ContentURI.CUString()
	} else {
		content.URL = uploaded.ContentURI.CUString()
	}
	portal.SetReply(content, msg.GetContextInfo().GetStanzaId())

	messageWithThumbnail, ok := msg.(MediaMessageWithThumbnail)
	if ok && messageWithThumbnail.GetJpegThumbnail() != nil && portal.bridge.Config.Bridge.WhatsappThumbnail {
		thumbnailData := messageWithThumbnail.GetJpegThumbnail()
		thumbnailMime := http.DetectContentType(thumbnailData)
		thumbnailCfg, _, _ := image.DecodeConfig(bytes.NewReader(thumbnailData))
		thumbnailSize := len(thumbnailData)
		thumbnail, thumbnailUploadMime, thumbnailFile := portal.encryptFile(thumbnailData, thumbnailMime)
		uploadedThumbnail, err := intent.UploadBytes(thumbnail, thumbnailUploadMime)
		if err != nil {
			portal.log.Warnfln("Failed to upload thumbnail for %s: %v", info.ID, err)
		} else if uploadedThumbnail != nil {
			if thumbnailFile != nil {
				thumbnailFile.URL = uploadedThumbnail.ContentURI.CUString()
				content.Info.ThumbnailFile = thumbnailFile
			} else {
				content.Info.ThumbnailURL = uploadedThumbnail.ContentURI.CUString()
			}
			content.Info.ThumbnailInfo = &event.FileInfo{
				Size:     thumbnailSize,
				Width:    thumbnailCfg.Width,
				Height:   thumbnailCfg.Height,
				MimeType: thumbnailMime,
			}
		}
	}

	_, isSticker := msg.(*waProto.StickerMessage)
	switch strings.ToLower(strings.Split(msg.GetMimetype(), "/")[0]) {
	case "image":
		if !isSticker {
			content.MsgType = event.MsgImage
		}
	case "video":
		content.MsgType = event.MsgVideo
	case "audio":
		content.MsgType = event.MsgAudio
	default:
		content.MsgType = event.MsgFile
	}

	eventType := event.EventMessage
	if isSticker {
		eventType = event.EventSticker
	}

	return &ConvertedMessage{
		Intent:  intent,
		Type:    eventType,
		Content: content,
		Caption: captionContent,
	}
}

func (portal *Portal) downloadThumbnail(content *event.MessageEventContent, id id.EventID) []byte {
	if len(content.GetInfo().ThumbnailURL) == 0 {
		return nil
	}
	mxc, err := content.GetInfo().ThumbnailURL.Parse()
	if err != nil {
		portal.log.Errorln("Malformed thumbnail URL in %s: %v", id, err)
	}
	thumbnail, err := portal.MainIntent().DownloadBytes(mxc)
	if err != nil {
		portal.log.Errorln("Failed to download thumbnail in %s: %v", id, err)
		return nil
	}
	thumbnailType := http.DetectContentType(thumbnail)
	var img image.Image
	switch thumbnailType {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(thumbnail))
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(thumbnail))
	case "image/jpeg":
		return thumbnail
	default:
		return nil
	}
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, img, &jpeg.Options{
		Quality: jpeg.DefaultQuality,
	})
	if err != nil {
		portal.log.Errorln("Failed to re-encode thumbnail in %s: %v", id, err)
		return nil
	}
	return buf.Bytes()
}

func (portal *Portal) convertWebPtoPNG(webpImage []byte) ([]byte, error) {
	webpDecoded, err := webp.Decode(bytes.NewReader(webpImage))
	if err != nil {
		return nil, fmt.Errorf("failed to decode webp image: %w", err)
	}

	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, webpDecoded); err != nil {
		return nil, fmt.Errorf("failed to encode png image: %w", err)
	}

	return pngBuffer.Bytes(), nil
}

func (portal *Portal) convertGifToVideo(gif []byte) ([]byte, error) {
	dir, err := ioutil.TempDir("", "gif-convert-*")
	if err != nil {
		return nil, fmt.Errorf("failed to make temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	inputFile, err := os.OpenFile(filepath.Join(dir, "input.gif"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed open input file: %w", err)
	}
	_, err = inputFile.Write(gif)
	if err != nil {
		_ = inputFile.Close()
		return nil, fmt.Errorf("failed to write gif to input file: %w", err)
	}
	_ = inputFile.Close()

	outputFileName := filepath.Join(dir, "output.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "warning",
		"-f", "gif", "-i", inputFile.Name(),
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-movflags", "+faststart",
		"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
		outputFileName)
	vcLog := portal.log.Sub("VideoConverter").Writer(log.LevelWarn)
	cmd.Stdout = vcLog
	cmd.Stderr = vcLog

	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg: %w", err)
	}
	outputFile, err := os.OpenFile(filepath.Join(dir, "output.mp4"), os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open output file: %w", err)
	}
	defer func() {
		_ = outputFile.Close()
		_ = os.Remove(outputFile.Name())
	}()
	mp4, err := ioutil.ReadAll(outputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read mp4 from output file: %w", err)
	}
	return mp4, nil
}

func (portal *Portal) preprocessMatrixMedia(sender *User, relaybotFormatted bool, content *event.MessageEventContent, eventID id.EventID, mediaType whatsmeow.MediaType) *MediaUpload {
	var caption string
	var mentionedJIDs []string
	if relaybotFormatted {
		caption, mentionedJIDs = portal.bridge.Formatter.ParseMatrix(content.FormattedBody)
	}

	var file *event.EncryptedFileInfo
	rawMXC := content.URL
	if content.File != nil {
		file = content.File
		rawMXC = file.URL
	}
	mxc, err := rawMXC.Parse()
	if err != nil {
		portal.log.Errorln("Malformed content URL in %s: %v", eventID, err)
		return nil
	}
	data, err := portal.MainIntent().DownloadBytes(mxc)
	if err != nil {
		portal.log.Errorfln("Failed to download media in %s: %v", eventID, err)
		return nil
	}
	if file != nil {
		data, err = file.Decrypt(data)
		if err != nil {
			portal.log.Errorfln("Failed to decrypt media in %s: %v", eventID, err)
			return nil
		}
	}
	if mediaType == whatsmeow.MediaVideo && content.GetInfo().MimeType == "image/gif" {
		data, err = portal.convertGifToVideo(data)
		if err != nil {
			portal.log.Errorfln("Failed to convert gif to mp4 in %s: %v", eventID, err)
			return nil
		}
		content.Info.MimeType = "video/mp4"
	}
	if mediaType == whatsmeow.MediaImage && content.GetInfo().MimeType == "image/webp" {
		data, err = portal.convertWebPtoPNG(data)
		if err != nil {
			portal.log.Errorfln("Failed to convert webp to png in %s: %v", eventID, err)
			return nil
		}
		content.Info.MimeType = "image/png"
	}
	uploadResp, err := sender.Client.Upload(context.Background(), data, mediaType)
	if err != nil {
		portal.log.Errorfln("Failed to upload media in %s: %v", eventID, err)
		return nil
	}

	return &MediaUpload{
		UploadResponse: uploadResp,
		Caption:        caption,
		MentionedJIDs:  mentionedJIDs,
		Thumbnail:      portal.downloadThumbnail(content, eventID),
		FileLength:     len(data),
	}
}

type MediaUpload struct {
	whatsmeow.UploadResponse
	Caption       string
	MentionedJIDs []string
	Thumbnail     []byte
	FileLength    int
}

func (portal *Portal) sendMatrixConnectionError(sender *User, eventID id.EventID) bool {
	if !sender.HasSession() {
		portal.log.Debugln("Ignoring event", eventID, "from", sender.MXID, "as user has no session")
		return true
	} /*else if !sender.IsConnected() {
		inRoom := ""
		if portal.IsPrivateChat() {
			inRoom = " in your management room"
		}
		if sender.IsLoginInProgress() {
			portal.log.Debugln("Waiting for connection before handling event", eventID, "from", sender.MXID)
			sender.Conn.WaitForLogin()
			if sender.IsConnected() {
				return false
			}
		}
		reconnect := fmt.Sprintf("Use `%s reconnect`%s to reconnect.", portal.bridge.Config.Bridge.CommandPrefix, inRoom)
		portal.log.Debugln("Ignoring event", eventID, "from", sender.MXID, "as user is not connected")
		msg := format.RenderMarkdown("\u26a0 You are not connected to WhatsApp, so your message was not bridged. "+reconnect, true, false)
		msg.MsgType = event.MsgNotice
		_, err := portal.sendMainIntentMessage(msg)
		if err != nil {
			portal.log.Errorln("Failed to send bridging failure message:", err)
		}
		return true
	}*/
	// FIXME implement
	return false
}

func (portal *Portal) addRelaybotFormat(sender *User, content *event.MessageEventContent) bool {
	member := portal.MainIntent().Member(portal.MXID, sender.MXID)
	if len(member.Displayname) == 0 {
		member.Displayname = string(sender.MXID)
	}

	if content.Format != event.FormatHTML {
		content.FormattedBody = strings.Replace(html.EscapeString(content.Body), "\n", "<br/>", -1)
		content.Format = event.FormatHTML
	}
	data, err := portal.bridge.Config.Bridge.Relaybot.FormatMessage(content, sender.MXID, member)
	if err != nil {
		portal.log.Errorln("Failed to apply relaybot format:", err)
	}
	content.FormattedBody = data
	return true
}

func addCodecToMime(mimeType, codec string) string {
	mediaType, params, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return mimeType
	}
	if _, ok := params["codecs"]; !ok {
		params["codecs"] = codec
	}
	return mime.FormatMediaType(mediaType, params)
}

func parseGeoURI(uri string) (lat, long float64, err error) {
	if !strings.HasPrefix(uri, "geo:") {
		err = fmt.Errorf("uri doesn't have geo: prefix")
		return
	}
	// Remove geo: prefix and anything after ;
	coordinates := strings.Split(strings.TrimPrefix(uri, "geo:"), ";")[0]

	if splitCoordinates := strings.Split(coordinates, ","); len(splitCoordinates) != 2 {
		err = fmt.Errorf("didn't find exactly two numbers separated by a comma")
	} else if lat, err = strconv.ParseFloat(splitCoordinates[0], 64); err != nil {
		err = fmt.Errorf("latitude is not a number: %w", err)
	} else if long, err = strconv.ParseFloat(splitCoordinates[1], 64); err != nil {
		err = fmt.Errorf("longitude is not a number: %w", err)
	}
	return
}

func fallbackQuoteContent() *waProto.Message {
	blankString := ""
	return &waProto.Message{
		Conversation: &blankString,
	}
}

func (portal *Portal) convertMatrixMessage(sender *User, evt *event.Event) (*waProto.Message, *User) {
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		portal.log.Debugfln("Failed to handle event %s: unexpected parsed content type %T", evt.ID, evt.Content.Parsed)
		return nil, sender
	}

	var msg waProto.Message
	var ctxInfo waProto.ContextInfo
	replyToID := content.GetReplyTo()
	if len(replyToID) > 0 {
		content.RemoveReplyFallback()
		replyToMsg := portal.bridge.DB.Message.GetByMXID(replyToID)
		if replyToMsg != nil {
			ctxInfo.StanzaId = &replyToMsg.JID
			ctxInfo.Participant = proto.String(replyToMsg.Sender.String())
			// Using blank content here seems to work fine on all official WhatsApp apps.
			// Getting the content from the phone would be possible, but it's complicated.
			// https://github.com/mautrix/whatsapp/commit/b3312bc663772aa274cea90ffa773da2217bb5e0
			ctxInfo.QuotedMessage = fallbackQuoteContent()
		}
	}
	relaybotFormatted := false
	if sender.NeedsRelaybot(portal) {
		if !portal.HasRelaybot() {
			if sender.HasSession() {
				portal.log.Debugln("Database says", sender.MXID, "not in chat and no relaybot, but trying to send anyway")
			} else {
				portal.log.Debugln("Ignoring message from", sender.MXID, "in chat with no relaybot")
				return nil, sender
			}
		} else {
			relaybotFormatted = portal.addRelaybotFormat(sender, content)
			sender = portal.bridge.Relaybot
		}
	}
	if evt.Type == event.EventSticker {
		content.MsgType = event.MsgImage
	}
	if content.MsgType == event.MsgImage && content.GetInfo().MimeType == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		text := content.Body
		if content.MsgType == event.MsgNotice && !portal.bridge.Config.Bridge.BridgeNotices {
			return nil, sender
		}
		if content.Format == event.FormatHTML {
			text, ctxInfo.MentionedJid = portal.bridge.Formatter.ParseMatrix(content.FormattedBody)
		}
		if content.MsgType == event.MsgEmote && !relaybotFormatted {
			text = "/me " + text
		}
		if ctxInfo.StanzaId != nil || ctxInfo.MentionedJid != nil {
			msg.ExtendedTextMessage = &waProto.ExtendedTextMessage{
				Text:        &text,
				ContextInfo: &ctxInfo,
			}
		} else {
			msg.Conversation = &text
		}
	case event.MsgImage:
		media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaImage)
		if media == nil {
			return nil, sender
		}
		ctxInfo.MentionedJid = media.MentionedJIDs
		msg.ImageMessage = &waProto.ImageMessage{
			ContextInfo:   &ctxInfo,
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
	case event.MsgVideo:
		gifPlayback := content.GetInfo().MimeType == "image/gif"
		media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaVideo)
		if media == nil {
			return nil, sender
		}
		duration := uint32(content.GetInfo().Duration / 1000)
		ctxInfo.MentionedJid = media.MentionedJIDs
		msg.VideoMessage = &waProto.VideoMessage{
			ContextInfo:   &ctxInfo,
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			GifPlayback:   &gifPlayback,
			Seconds:       &duration,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
	case event.MsgAudio:
		media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaAudio)
		if media == nil {
			return nil, sender
		}
		duration := uint32(content.GetInfo().Duration / 1000)
		msg.AudioMessage = &waProto.AudioMessage{
			ContextInfo:   &ctxInfo,
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			Seconds:       &duration,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
		_, isMSC3245Voice := evt.Content.Raw["org.matrix.msc3245.voice"]
		_, isMSC2516Voice := evt.Content.Raw["org.matrix.msc2516.voice"]
		if isMSC3245Voice || isMSC2516Voice {
			msg.AudioMessage.Ptt = proto.Bool(true)
			// hacky hack to add the codecs param that whatsapp seems to require
			msg.AudioMessage.Mimetype = proto.String(addCodecToMime(content.GetInfo().MimeType, "opus"))
		}
	case event.MsgFile:
		media := portal.preprocessMatrixMedia(sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaDocument)
		if media == nil {
			return nil, sender
		}
		msg.DocumentMessage = &waProto.DocumentMessage{
			ContextInfo:   &ctxInfo,
			Url:           &media.URL,
			Title:         &content.Body,
			FileName:      &content.Body,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
	case event.MsgLocation:
		lat, long, err := parseGeoURI(content.GeoURI)
		if err != nil {
			portal.log.Debugfln("Invalid geo URI on Matrix event %s: %v", evt.ID, err)
			return nil, sender
		}
		msg.LocationMessage = &waProto.LocationMessage{
			DegreesLatitude:  &lat,
			DegreesLongitude: &long,
			Comment:          &content.Body,
			ContextInfo:      &ctxInfo,
		}
	default:
		portal.log.Debugfln("Unhandled Matrix event %s: unknown msgtype %s", evt.ID, content.MsgType)
		return nil, sender
	}
	return &msg, sender
}

func (portal *Portal) sendErrorMessage(message string, confirmed bool) id.EventID {
	certainty := "may not have been"
	if confirmed {
		certainty = "was not"
	}
	resp, err := portal.sendMainIntentMessage(event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("\u26a0 Your message %s bridged: %v", certainty, message),
	})
	if err != nil {
		portal.log.Warnfln("Failed to send bridging error message:", err)
		return ""
	}
	return resp.EventID
}

func (portal *Portal) sendDeliveryReceipt(eventID id.EventID) {
	if portal.bridge.Config.Bridge.DeliveryReceipts {
		err := portal.bridge.Bot.MarkRead(portal.MXID, eventID)
		if err != nil {
			portal.log.Debugfln("Failed to send delivery receipt for %s: %v", eventID, err)
		}
	}
}

func (portal *Portal) generateMessageInfo(sender *User) *types.MessageInfo {
	return &types.MessageInfo{
		ID:        whatsmeow.GenerateMessageID(),
		Timestamp: time.Now(),
		MessageSource: types.MessageSource{
			Sender:   sender.JID,
			Chat:     portal.Key.JID,
			IsFromMe: true,
			IsGroup:  portal.Key.JID.Server == types.GroupServer || portal.Key.JID.Server == types.BroadcastServer,
		},
	}
}

func (portal *Portal) HandleMatrixMessage(sender *User, evt *event.Event) {
	if !portal.HasRelaybot() &&
		((portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User) ||
			portal.sendMatrixConnectionError(sender, evt.ID)) {
		return
	}
	portal.log.Debugfln("Received event %s", evt.ID)
	msg, sender := portal.convertMatrixMessage(sender, evt)
	if msg == nil {
		return
	}
	info := portal.generateMessageInfo(sender)
	dbMsg := portal.markHandled(nil, info, evt.ID, false, true, false)
	portal.log.Debugln("Sending event", evt.ID, "to WhatsApp", info.ID)
	err := sender.Client.SendMessage(portal.Key.JID, info.ID, msg)
	if err != nil {
		portal.log.Errorln("Error sending message: %v", err)
		portal.sendErrorMessage(err.Error(), true)
	} else {
		portal.log.Debugfln("Handled Matrix event %s", evt.ID)
		portal.sendDeliveryReceipt(evt.ID)
		dbMsg.MarkSent()
	}
}

func (portal *Portal) HandleMatrixRedaction(sender *User, evt *event.Event) {
	if portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User {
		return
	}

	msg := portal.bridge.DB.Message.GetByMXID(evt.Redacts)
	if msg == nil || msg.Sender.User != sender.JID.User {
		return
	}

	portal.log.Debugfln("Received redaction event %s", evt.ID)
	info := portal.generateMessageInfo(sender)
	portal.log.Debugln("Sending redaction", evt.ID, "to WhatsApp", info.ID)
	err := sender.Client.SendMessage(portal.Key.JID, info.ID, &waProto.Message{
		ProtocolMessage: &waProto.ProtocolMessage{
			Type: waProto.ProtocolMessage_REVOKE.Enum(),
			Key: &waProto.MessageKey{
				FromMe:    proto.Bool(true),
				Id:        proto.String(msg.JID),
				RemoteJid: proto.String(portal.Key.JID.String()),
			},
		},
	})
	if err != nil {
		portal.log.Errorfln("Error handling Matrix redaction %s: %v", evt.ID, err)
	} else {
		portal.log.Debugln("Handled Matrix redaction %s of %s", evt.ID, evt.Redacts)
		portal.sendDeliveryReceipt(evt.ID)
	}
}

func (portal *Portal) Delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByJID, portal.Key)
	if len(portal.MXID) > 0 {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) GetMatrixUsers() ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(portal.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to get member list: %w", err)
	}
	var users []id.UserID
	for userID := range members.Joined {
		_, isPuppet := portal.bridge.ParsePuppetMXID(userID)
		if !isPuppet && userID != portal.bridge.Bot.UserID {
			users = append(users, userID)
		}
	}
	return users, nil
}

func (portal *Portal) CleanupIfEmpty() {
	users, err := portal.GetMatrixUsers()
	if err != nil {
		portal.log.Errorfln("Failed to get Matrix user list to determine if portal needs to be cleaned up: %v", err)
		return
	}

	if len(users) == 0 {
		portal.log.Infoln("Room seems to be empty, cleaning up...")
		portal.Delete()
		portal.Cleanup(false)
	}
}

func (portal *Portal) Cleanup(puppetsOnly bool) {
	if len(portal.MXID) == 0 {
		return
	}
	if portal.IsPrivateChat() {
		_, err := portal.MainIntent().LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnln("Failed to leave private chat portal with main intent:", err)
		}
		return
	}
	intent := portal.MainIntent()
	members, err := intent.JoinedMembers(portal.MXID)
	if err != nil {
		portal.log.Errorln("Failed to get portal members for cleanup:", err)
		return
	}
	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}
		puppet := portal.bridge.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(portal.MXID)
			if err != nil {
				portal.log.Errorln("Error leaving as puppet while cleaning up portal:", err)
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				portal.log.Errorln("Error kicking user while cleaning up portal:", err)
			}
		}
	}
	_, err = intent.LeaveRoom(portal.MXID)
	if err != nil {
		portal.log.Errorln("Error leaving with main intent while cleaning up portal:", err)
	}
}

func (portal *Portal) HandleMatrixLeave(sender *User) {
	if portal.IsPrivateChat() {
		portal.log.Debugln("User left private chat portal, cleaning up and deleting...")
		portal.Delete()
		portal.Cleanup(false)
		return
	} else if portal.bridge.Config.Bridge.BridgeMatrixLeave {
		// TODO should we somehow deduplicate this call if this leave was sent by the bridge?
		// FIXME reimplement
		//resp, err := sender.Client.LeaveGroup(portal.Key.JID)
		//if err != nil {
		//	portal.log.Errorfln("Failed to leave group as %s: %v", sender.MXID, err)
		//	return
		//}
		//portal.log.Infoln("Leave response:", <-resp)
	}
	portal.CleanupIfEmpty()
}

func (portal *Portal) HandleMatrixKick(sender *User, evt *event.Event) {
	puppet := portal.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	if puppet != nil {
		// FIXME reimplement
		//resp, err := sender.Conn.RemoveMember(portal.Key.JID, []string{puppet.JID})
		//if err != nil {
		//	portal.log.Errorfln("Failed to kick %s from group as %s: %v", puppet.JID, sender.MXID, err)
		//	return
		//}
		//portal.log.Infoln("Kick %s response: %s", puppet.JID, <-resp)
	}
}

func (portal *Portal) HandleMatrixInvite(sender *User, evt *event.Event) {
	puppet := portal.bridge.GetPuppetByMXID(id.UserID(evt.GetStateKey()))
	if puppet != nil {
		// FIXME reimplement
		//resp, err := sender.Conn.AddMember(portal.Key.JID, []string{puppet.JID})
		//if err != nil {
		//	portal.log.Errorfln("Failed to add %s to group as %s: %v", puppet.JID, sender.MXID, err)
		//	return
		//}
		//portal.log.Infofln("Add %s response: %s", puppet.JID, <-resp)
	}
}

func (portal *Portal) HandleMatrixMeta(sender *User, evt *event.Event) {
	var err error
	switch content := evt.Content.Parsed.(type) {
	case *event.RoomNameEventContent:
		if content.Name == portal.Name {
			return
		}
		// FIXME reimplement
		//portal.Name = content.Name
		//resp, err = sender.Conn.UpdateGroupSubject(content.Name, portal.Key.JID)
	case *event.TopicEventContent:
		if content.Topic == portal.Topic {
			return
		}
		// FIXME reimplement
		//portal.Topic = content.Topic
		//resp, err = sender.Conn.UpdateGroupDescription(sender.JID, portal.Key.JID, content.Topic)
	case *event.RoomAvatarEventContent:
		return
	}
	if err != nil {
		portal.log.Errorln("Failed to update metadata:", err)
	} else {
		//out := <-resp
		//portal.log.Debugln("Successfully updated metadata:", out)
	}
}
