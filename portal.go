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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util"
	"maunium.net/go/mautrix/util/ffmpeg"
	"maunium.net/go/mautrix/util/variationselector"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

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

func (bridge *Bridge) GetAllPortalsForUser(userID id.UserID) []*Portal {
	return bridge.dbPortalsToPortals(bridge.DB.Portal.GetAllForUser(userID))
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

func (bridge *Bridge) newBlankPortal(key database.PortalKey) *Portal {
	portal := &Portal{
		bridge: bridge,
		log:    bridge.Log.Sub(fmt.Sprintf("Portal/%s", key)),

		messages:       make(chan PortalMessage, bridge.Config.Bridge.PortalMessageBuffer),
		matrixMessages: make(chan PortalMatrixMessage, bridge.Config.Bridge.PortalMessageBuffer),
		mediaRetries:   make(chan PortalMediaRetry, bridge.Config.Bridge.PortalMessageBuffer),

		mediaErrorCache: make(map[types.MessageID]*FailedMediaMeta),
	}
	go portal.handleMessageLoop()
	return portal
}

func (bridge *Bridge) NewManualPortal(key database.PortalKey) *Portal {
	portal := bridge.newBlankPortal(key)
	portal.Portal = bridge.DB.Portal.New()
	portal.Key = key
	return portal
}

func (bridge *Bridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := bridge.newBlankPortal(dbPortal.Key)
	portal.Portal = dbPortal
	return portal
}

const recentlyHandledLength = 100

type fakeMessage struct {
	Sender    types.JID
	Text      string
	ID        string
	Time      time.Time
	Important bool
}

type PortalMessage struct {
	evt           *events.Message
	undecryptable *events.UndecryptableMessage
	receipt       *events.Receipt
	fake          *fakeMessage
	source        *User
}

type PortalMatrixMessage struct {
	evt  *event.Event
	user *User
}

type PortalMediaRetry struct {
	evt    *events.MediaRetry
	source *User
}

type recentlyHandledWrapper struct {
	id  types.MessageID
	err database.MessageErrorType
}

type Portal struct {
	*database.Portal

	bridge *Bridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex
	backfillLock   sync.Mutex
	avatarLock     sync.Mutex

	recentlyHandled      [recentlyHandledLength]recentlyHandledWrapper
	recentlyHandledLock  sync.Mutex
	recentlyHandledIndex uint8

	privateChatBackfillInvitePuppet func()

	currentlyTyping     []id.UserID
	currentlyTypingLock sync.Mutex

	messages       chan PortalMessage
	matrixMessages chan PortalMatrixMessage
	mediaRetries   chan PortalMediaRetry

	mediaErrorCache map[types.MessageID]*FailedMediaMeta

	relayUser *User
}

func (portal *Portal) handleMessageLoopItem(msg PortalMessage) {
	if len(portal.MXID) == 0 {
		if msg.fake == nil && msg.undecryptable == nil && (msg.evt == nil || !containsSupportedMessage(msg.evt.Message)) {
			portal.log.Debugln("Not creating portal room for incoming message: message is not a chat message")
			return
		}
		portal.log.Debugln("Creating Matrix room from incoming message")
		err := portal.CreateMatrixRoom(msg.source, nil, false, true)
		if err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return
		}
	}
	if msg.evt != nil {
		portal.handleMessage(msg.source, msg.evt)
	} else if msg.receipt != nil {
		portal.handleReceipt(msg.receipt, msg.source)
	} else if msg.undecryptable != nil {
		portal.handleUndecryptableMessage(msg.source, msg.undecryptable)
	} else if msg.fake != nil {
		msg.fake.ID = "FAKE::" + msg.fake.ID
		portal.handleFakeMessage(*msg.fake)
	} else {
		portal.log.Warnln("Unexpected PortalMessage with no message: %+v", msg)
	}
}

func (portal *Portal) handleMatrixMessageLoopItem(msg PortalMatrixMessage) {
	portal.HandleMatrixReadReceipt(msg.user, "", time.UnixMilli(msg.evt.Timestamp), false)
	switch msg.evt.Type {
	case event.EventMessage, event.EventSticker:
		portal.HandleMatrixMessage(msg.user, msg.evt)
	case event.EventRedaction:
		portal.HandleMatrixRedaction(msg.user, msg.evt)
	default:
		portal.log.Warnln("Unsupported event type %+v in portal message channel", msg.evt.Type)
	}
}

func (portal *Portal) handleReceipt(receipt *events.Receipt, source *User) {
	// The order of the message ID array depends on the sender's platform, so we just have to find
	// the last message based on timestamp. Also, timestamps only have second precision, so if
	// there are many messages at the same second just mark them all as read, because we don't
	// know which one is last
	markAsRead := make([]*database.Message, 0, 1)
	var bestTimestamp time.Time
	for _, msgID := range receipt.MessageIDs {
		msg := portal.bridge.DB.Message.GetByJID(portal.Key, msgID)
		if msg == nil || msg.IsFakeMXID() {
			continue
		}
		if msg.Timestamp.After(bestTimestamp) {
			bestTimestamp = msg.Timestamp
			markAsRead = append(markAsRead[:0], msg)
		} else if msg != nil && msg.Timestamp.Equal(bestTimestamp) {
			markAsRead = append(markAsRead, msg)
		}
	}
	if receipt.Sender.User == source.JID.User {
		if len(markAsRead) > 0 {
			source.SetLastReadTS(portal.Key, markAsRead[0].Timestamp)
		} else {
			source.SetLastReadTS(portal.Key, receipt.Timestamp)
		}
	}
	intent := portal.bridge.GetPuppetByJID(receipt.Sender).IntentFor(portal)
	for _, msg := range markAsRead {
		err := intent.SetReadMarkers(portal.MXID, makeReadMarkerContent(msg.MXID, intent.IsCustomPuppet))
		if err != nil {
			portal.log.Warnfln("Failed to mark message %s as read by %s: %v", msg.MXID, intent.UserID, err)
		} else {
			portal.log.Debugfln("Marked %s as read by %s", msg.MXID, intent.UserID)
		}
	}
}

func (portal *Portal) handleMessageLoop() {
	for {
		select {
		case msg := <-portal.messages:
			portal.handleMessageLoopItem(msg)
		case msg := <-portal.matrixMessages:
			portal.handleMatrixMessageLoopItem(msg)
		case retry := <-portal.mediaRetries:
			portal.handleMediaRetry(retry.evt, retry.source)
		}
	}
}

func containsSupportedMessage(waMsg *waProto.Message) bool {
	if waMsg == nil {
		return false
	}
	return waMsg.Conversation != nil || waMsg.ExtendedTextMessage != nil || waMsg.ImageMessage != nil ||
		waMsg.StickerMessage != nil || waMsg.AudioMessage != nil || waMsg.VideoMessage != nil ||
		waMsg.DocumentMessage != nil || waMsg.ContactMessage != nil || waMsg.LocationMessage != nil ||
		waMsg.LiveLocationMessage != nil || waMsg.GroupInviteMessage != nil || waMsg.ContactsArrayMessage != nil
}

func getMessageType(waMsg *waProto.Message) string {
	switch {
	case waMsg == nil:
		return "ignore"
	case waMsg.Conversation != nil, waMsg.ExtendedTextMessage != nil:
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
	case waMsg.ContactsArrayMessage != nil:
		return "contact array"
	case waMsg.LocationMessage != nil:
		return "location"
	case waMsg.LiveLocationMessage != nil:
		return "live location start"
	case waMsg.GroupInviteMessage != nil:
		return "group invite"
	case waMsg.ReactionMessage != nil:
		return "reaction"
	case waMsg.ProtocolMessage != nil:
		switch waMsg.GetProtocolMessage().GetType() {
		case waProto.ProtocolMessage_REVOKE:
			if waMsg.GetProtocolMessage().GetKey() == nil {
				return "ignore"
			}
			return "revoke"
		case waProto.ProtocolMessage_EPHEMERAL_SETTING:
			return "disappearing timer change"
		case waProto.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE, waProto.ProtocolMessage_HISTORY_SYNC_NOTIFICATION, waProto.ProtocolMessage_INITIAL_SECURITY_NOTIFICATION_SETTING_SYNC:
			return "ignore"
		default:
			return fmt.Sprintf("unknown_protocol_%d", waMsg.GetProtocolMessage().GetType())
		}
	case waMsg.ButtonsMessage != nil:
		return "buttons"
	case waMsg.ButtonsResponseMessage != nil:
		return "buttons response"
	case waMsg.TemplateMessage != nil:
		return "template"
	case waMsg.HighlyStructuredMessage != nil:
		return "highly structured template"
	case waMsg.TemplateButtonReplyMessage != nil:
		return "template button reply"
	case waMsg.InteractiveMessage != nil:
		return "interactive"
	case waMsg.ListMessage != nil:
		return "list"
	case waMsg.ProductMessage != nil:
		return "product"
	case waMsg.ListResponseMessage != nil:
		return "list response"
	case waMsg.OrderMessage != nil:
		return "order"
	case waMsg.InvoiceMessage != nil:
		return "invoice"
	case waMsg.SendPaymentMessage != nil, waMsg.RequestPaymentMessage != nil,
		waMsg.DeclinePaymentRequestMessage != nil, waMsg.CancelPaymentRequestMessage != nil,
		waMsg.PaymentInviteMessage != nil:
		return "payment"
	case waMsg.Call != nil:
		return "call"
	case waMsg.Chat != nil:
		return "chat"
	case waMsg.SenderKeyDistributionMessage != nil, waMsg.StickerSyncRmrMessage != nil:
		return "ignore"
	default:
		return "unknown"
	}
}

func pluralUnit(val int, name string) string {
	if val == 1 {
		return fmt.Sprintf("%d %s", val, name)
	} else if val == 0 {
		return ""
	}
	return fmt.Sprintf("%d %ss", val, name)
}

func naturalJoin(parts []string) string {
	if len(parts) == 0 {
		return ""
	} else if len(parts) == 1 {
		return parts[0]
	} else if len(parts) == 2 {
		return fmt.Sprintf("%s and %s", parts[0], parts[1])
	} else {
		return fmt.Sprintf("%s and %s", strings.Join(parts[:len(parts)-1], ", "), parts[len(parts)-1])
	}
}

func formatDuration(d time.Duration) string {
	const Day = time.Hour * 24

	var days, hours, minutes, seconds int
	days, d = int(d/Day), d%Day
	hours, d = int(d/time.Hour), d%time.Hour
	minutes, d = int(d/time.Minute), d%time.Minute
	seconds = int(d / time.Second)

	parts := make([]string, 0, 4)
	if days > 0 {
		parts = append(parts, pluralUnit(days, "day"))
	}
	if hours > 0 {
		parts = append(parts, pluralUnit(hours, "hour"))
	}
	if minutes > 0 {
		parts = append(parts, pluralUnit(seconds, "minute"))
	}
	if seconds > 0 {
		parts = append(parts, pluralUnit(seconds, "second"))
	}
	return naturalJoin(parts)
}

func (portal *Portal) convertMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, waMsg *waProto.Message, isBackfill bool) *ConvertedMessage {
	switch {
	case waMsg.Conversation != nil || waMsg.ExtendedTextMessage != nil:
		return portal.convertTextMessage(intent, source, waMsg)
	case waMsg.ImageMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetImageMessage(), "photo", isBackfill)
	case waMsg.StickerMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetStickerMessage(), "sticker", isBackfill)
	case waMsg.VideoMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetVideoMessage(), "video attachment", isBackfill)
	case waMsg.AudioMessage != nil:
		typeName := "audio attachment"
		if waMsg.GetAudioMessage().GetPtt() {
			typeName = "voice message"
		}
		return portal.convertMediaMessage(intent, source, info, waMsg.GetAudioMessage(), typeName, isBackfill)
	case waMsg.DocumentMessage != nil:
		return portal.convertMediaMessage(intent, source, info, waMsg.GetDocumentMessage(), "file attachment", isBackfill)
	case waMsg.ContactMessage != nil:
		return portal.convertContactMessage(intent, waMsg.GetContactMessage())
	case waMsg.ContactsArrayMessage != nil:
		return portal.convertContactsArrayMessage(intent, waMsg.GetContactsArrayMessage())
	case waMsg.LocationMessage != nil:
		return portal.convertLocationMessage(intent, waMsg.GetLocationMessage())
	case waMsg.LiveLocationMessage != nil:
		return portal.convertLiveLocationMessage(intent, waMsg.GetLiveLocationMessage())
	case waMsg.GroupInviteMessage != nil:
		return portal.convertGroupInviteMessage(intent, info, waMsg.GetGroupInviteMessage())
	case waMsg.ProtocolMessage != nil && waMsg.ProtocolMessage.GetType() == waProto.ProtocolMessage_EPHEMERAL_SETTING:
		portal.ExpirationTime = waMsg.ProtocolMessage.GetEphemeralExpiration()
		portal.Update(nil)
		return &ConvertedMessage{
			Intent: intent,
			Type:   event.EventMessage,
			Content: &event.MessageEventContent{
				Body:    portal.formatDisappearingMessageNotice(),
				MsgType: event.MsgNotice,
			},
		}
	default:
		return nil
	}
}

func (portal *Portal) UpdateGroupDisappearingMessages(sender *types.JID, timestamp time.Time, timer uint32) {
	if portal.ExpirationTime == timer {
		return
	}
	portal.ExpirationTime = timer
	portal.Update(nil)
	intent := portal.MainIntent()
	if sender != nil {
		intent = portal.bridge.GetPuppetByJID(sender.ToNonAD()).IntentFor(portal)
	} else {
		sender = &types.EmptyJID
	}
	_, err := portal.sendMessage(intent, event.EventMessage, &event.MessageEventContent{
		Body:    portal.formatDisappearingMessageNotice(),
		MsgType: event.MsgNotice,
	}, nil, timestamp.UnixMilli())
	if err != nil {
		portal.log.Warnfln("Failed to notify portal about disappearing message timer change by %s to %d", *sender, timer)
	}
}

func (portal *Portal) formatDisappearingMessageNotice() string {
	if portal.ExpirationTime == 0 {
		return "Turned off disappearing messages"
	} else {
		msg := fmt.Sprintf("Set the disappearing message timer to %s", formatDuration(time.Duration(portal.ExpirationTime)*time.Second))
		if !portal.bridge.Config.Bridge.DisappearingMessagesInGroups && portal.IsGroupChat() {
			msg += ". However, this bridge is not configured to disappear messages in group chats."
		}
		return msg
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
	} else if portal.isRecentlyHandled(evt.Info.ID, database.MsgErrDecryptionFailed) {
		portal.log.Debugfln("Not handling %s (undecryptable): message was recently handled", evt.Info.ID)
		return
	} else if existingMsg := portal.bridge.DB.Message.GetByJID(portal.Key, evt.Info.ID); existingMsg != nil {
		portal.log.Debugfln("Not handling %s (undecryptable): message is duplicate", evt.Info.ID)
		return
	}
	metricType := "error"
	if evt.IsUnavailable {
		metricType = "unavailable"
	}
	Segment.Track(source.MXID, "WhatsApp undecryptable message", map[string]interface{}{
		"messageID":         evt.Info.ID,
		"undecryptableType": metricType,
	})
	intent := portal.getMessageIntent(source, &evt.Info)
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && evt.Info.Sender.User == portal.Key.Receiver.User {
		portal.log.Debugfln("Not handling %s (undecryptable): user doesn't have double puppeting enabled", evt.Info.ID)
		return
	}
	content := undecryptableMessageContent
	resp, err := portal.sendMessage(intent, event.EventMessage, &content, nil, evt.Info.Timestamp.UnixMilli())
	if err != nil {
		portal.log.Errorln("Failed to send decryption error of %s to Matrix: %v", evt.Info.ID, err)
		return
	}
	portal.finishHandling(nil, &evt.Info, resp.EventID, database.MsgUnknown, database.MsgErrDecryptionFailed)
}

func (portal *Portal) handleFakeMessage(msg fakeMessage) {
	if portal.isRecentlyHandled(msg.ID, database.MsgNoError) {
		portal.log.Debugfln("Not handling %s (fake): message was recently handled", msg.ID)
		return
	} else if existingMsg := portal.bridge.DB.Message.GetByJID(portal.Key, msg.ID); existingMsg != nil {
		portal.log.Debugfln("Not handling %s (fake): message is duplicate", msg.ID)
		return
	}
	intent := portal.bridge.GetPuppetByJID(msg.Sender).IntentFor(portal)
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && msg.Sender.User == portal.Key.Receiver.User {
		portal.log.Debugfln("Not handling %s (fake): user doesn't have double puppeting enabled", msg.ID)
		return
	}
	msgType := event.MsgNotice
	if msg.Important {
		msgType = event.MsgText
	}
	resp, err := portal.sendMessage(intent, event.EventMessage, &event.MessageEventContent{
		MsgType: msgType,
		Body:    msg.Text,
	}, nil, msg.Time.UnixMilli())
	if err != nil {
		portal.log.Errorfln("Failed to send %s to Matrix: %v", msg.ID, err)
	} else {
		portal.finishHandling(nil, &types.MessageInfo{
			ID:        msg.ID,
			Timestamp: msg.Time,
			MessageSource: types.MessageSource{
				Sender: msg.Sender,
			},
		}, resp.EventID, database.MsgFake, database.MsgNoError)
	}
}

func (portal *Portal) handleMessage(source *User, evt *events.Message) {
	if len(portal.MXID) == 0 {
		portal.log.Warnln("handleMessage called even though portal.MXID is empty")
		return
	}
	msgID := evt.Info.ID
	msgType := getMessageType(evt.Message)
	if msgType == "ignore" {
		return
	} else if portal.isRecentlyHandled(msgID, database.MsgNoError) {
		portal.log.Debugfln("Not handling %s (%s): message was recently handled", msgID, msgType)
		return
	}
	existingMsg := portal.bridge.DB.Message.GetByJID(portal.Key, msgID)
	if existingMsg != nil {
		if existingMsg.Error == database.MsgErrDecryptionFailed {
			Segment.Track(source.MXID, "WhatsApp undecryptable message resolved", map[string]interface{}{
				"messageID": evt.Info.ID,
			})
			portal.log.Debugfln("Got decryptable version of previously undecryptable message %s (%s)", msgID, msgType)
		} else {
			portal.log.Debugfln("Not handling %s (%s): message is duplicate", msgID, msgType)
			return
		}
	}

	intent := portal.getMessageIntent(source, &evt.Info)
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && evt.Info.Sender.User == portal.Key.Receiver.User {
		portal.log.Debugfln("Not handling %s (%s): user doesn't have double puppeting enabled", msgID, msgType)
		return
	}
	converted := portal.convertMessage(intent, source, &evt.Info, evt.Message, false)
	if converted != nil {
		if evt.Info.IsIncomingBroadcast() {
			if converted.Extra == nil {
				converted.Extra = map[string]interface{}{}
			}
			converted.Extra["fi.mau.whatsapp.source_broadcast_list"] = evt.Info.Chat.String()
		}
		var eventID id.EventID
		if existingMsg != nil {
			portal.MarkDisappearing(existingMsg.MXID, converted.ExpiresIn, false)
			converted.Content.SetEdit(existingMsg.MXID)
		} else if len(converted.ReplyTo) > 0 {
			portal.SetReply(converted.Content, converted.ReplyTo)
		}
		resp, err := portal.sendMessage(converted.Intent, converted.Type, converted.Content, converted.Extra, evt.Info.Timestamp.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Failed to send %s to Matrix: %v", msgID, err)
		} else {
			portal.MarkDisappearing(resp.EventID, converted.ExpiresIn, false)
			eventID = resp.EventID
		}
		// TODO figure out how to handle captions with undecryptable messages turning decryptable
		if converted.Caption != nil && existingMsg == nil {
			resp, err = portal.sendMessage(converted.Intent, converted.Type, converted.Caption, nil, evt.Info.Timestamp.UnixMilli())
			if err != nil {
				portal.log.Errorfln("Failed to send caption of %s to Matrix: %v", msgID, err)
			} else {
				portal.MarkDisappearing(resp.EventID, converted.ExpiresIn, false)
				//eventID = resp.EventID
			}
		}
		if converted.MultiEvent != nil && existingMsg == nil {
			for index, subEvt := range converted.MultiEvent {
				resp, err = portal.sendMessage(converted.Intent, converted.Type, subEvt, nil, evt.Info.Timestamp.UnixMilli())
				if err != nil {
					portal.log.Errorfln("Failed to send sub-event %d of %s to Matrix: %v", index+1, msgID, err)
				} else {
					portal.MarkDisappearing(resp.EventID, converted.ExpiresIn, false)
				}
			}
		}
		if len(eventID) != 0 {
			portal.finishHandling(existingMsg, &evt.Info, eventID, database.MsgNormal, converted.Error)
		}
	} else if msgType == "reaction" {
		portal.HandleMessageReaction(intent, source, &evt.Info, evt.Message.GetReactionMessage(), existingMsg)
	} else if msgType == "revoke" {
		portal.HandleMessageRevoke(source, &evt.Info, evt.Message.GetProtocolMessage().GetKey())
		if existingMsg != nil {
			_, _ = portal.MainIntent().RedactEvent(portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
				Reason: "The undecryptable message was actually the deletion of another message",
			})
			existingMsg.UpdateMXID(nil, "net.maunium.whatsapp.fake::"+existingMsg.MXID, database.MsgFake, database.MsgNoError)
		}
	} else {
		portal.log.Warnfln("Unhandled message: %+v (%s)", evt.Info, msgType)
		if existingMsg != nil {
			_, _ = portal.MainIntent().RedactEvent(portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
				Reason: "The undecryptable message contained an unsupported message type",
			})
			existingMsg.UpdateMXID(nil, "net.maunium.whatsapp.fake::"+existingMsg.MXID, database.MsgFake, database.MsgNoError)
		}
		return
	}
	portal.bridge.Metrics.TrackWhatsAppMessage(evt.Info.Timestamp, strings.Split(msgType, " ")[0])
}

func (portal *Portal) isRecentlyHandled(id types.MessageID, error database.MessageErrorType) bool {
	start := portal.recentlyHandledIndex
	lookingForMsg := recentlyHandledWrapper{id, error}
	for i := start; i != start; i = (i - 1) % recentlyHandledLength {
		if portal.recentlyHandled[i] == lookingForMsg {
			return true
		}
	}
	return false
}

func (portal *Portal) markHandled(txn *sql.Tx, msg *database.Message, info *types.MessageInfo, mxid id.EventID, isSent, recent bool, msgType database.MessageType, errType database.MessageErrorType) *database.Message {
	if msg == nil {
		msg = portal.bridge.DB.Message.New()
		msg.Chat = portal.Key
		msg.JID = info.ID
		msg.MXID = mxid
		msg.Timestamp = info.Timestamp
		msg.Sender = info.Sender
		msg.Sent = isSent
		msg.Type = msgType
		msg.Error = errType
		if info.IsIncomingBroadcast() {
			msg.BroadcastListJID = info.Chat
		}
		msg.Insert(txn)
	} else {
		msg.UpdateMXID(txn, mxid, msgType, errType)
	}

	if recent {
		portal.recentlyHandledLock.Lock()
		index := portal.recentlyHandledIndex
		portal.recentlyHandledIndex = (portal.recentlyHandledIndex + 1) % recentlyHandledLength
		portal.recentlyHandledLock.Unlock()
		portal.recentlyHandled[index] = recentlyHandledWrapper{msg.JID, errType}
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
		puppet.SyncContact(user, true, true, "handling message")
		return puppet
	}
}

func (portal *Portal) getMessageIntent(user *User, info *types.MessageInfo) *appservice.IntentAPI {
	return portal.getMessagePuppet(user, info).IntentFor(portal)
}

func (portal *Portal) finishHandling(existing *database.Message, message *types.MessageInfo, mxid id.EventID, msgType database.MessageType, errType database.MessageErrorType) {
	portal.markHandled(nil, existing, message, mxid, true, true, msgType, errType)
	portal.sendDeliveryReceipt(mxid)
	var suffix string
	if errType == database.MsgErrDecryptionFailed {
		suffix = "(undecryptable message error notice)"
	} else if errType == database.MsgErrMediaNotFound {
		suffix = "(media not found notice)"
	}
	portal.log.Debugfln("Handled message %s (%s) -> %s %s", message.ID, msgType, mxid, suffix)
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
	changed = portal.applyPowerLevelFixes(levels) || changed
	participantMap := make(map[types.JID]bool)
	for _, participant := range metadata.Participants {
		participantMap[participant.JID] = true
		puppet := portal.bridge.GetPuppetByJID(participant.JID)
		puppet.SyncContact(source, true, false, "group participant")
		user := portal.bridge.GetUserByJID(participant.JID)
		if user != nil && user != source {
			portal.ensureUserInvited(user)
		}
		if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
			err = puppet.IntentFor(portal).EnsureJoined(portal.MXID)
			if err != nil {
				portal.log.Warnfln("Failed to make puppet of %s join %s: %v", participant.JID, portal.MXID, err)
			}
		}

		expectedLevel := 0
		if participant.IsSuperAdmin {
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

func (portal *Portal) UpdateAvatar(user *User, setBy types.JID, updateInfo bool) bool {
	portal.avatarLock.Lock()
	defer portal.avatarLock.Unlock()
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
		intent := portal.MainIntent()
		if !setBy.IsEmpty() {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
		}
		_, err = intent.SetRoomAvatar(portal.MXID, portal.AvatarURL)
		if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
			_, err = portal.MainIntent().SetRoomAvatar(portal.MXID, portal.AvatarURL)
		}
		if err != nil {
			portal.log.Warnln("Failed to set room avatar:", err)
			return false
		}
	}
	portal.Avatar = avatar.ID
	if updateInfo {
		portal.UpdateBridgeInfo()
	}
	return true
}

func (portal *Portal) UpdateName(name string, setBy types.JID, updateInfo bool) bool {
	if name == "" && portal.IsBroadcastList() {
		name = UnnamedBroadcastName
	}
	if portal.Name != name {
		portal.log.Debugfln("Updating name %s -> %s", portal.Name, name)
		portal.Name = name

		intent := portal.MainIntent()
		if !setBy.IsEmpty() {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomName(portal.MXID, name)
		if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
			_, err = portal.MainIntent().SetRoomName(portal.MXID, name)
		}
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

func (portal *Portal) UpdateTopic(topic string, setBy types.JID, updateInfo bool) bool {
	if portal.Topic != topic {
		portal.log.Debugfln("Updating topic %s -> %s", portal.Topic, topic)
		portal.Topic = topic

		intent := portal.MainIntent()
		if !setBy.IsEmpty() {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomTopic(portal.MXID, topic)
		if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
			_, err = portal.MainIntent().SetRoomTopic(portal.MXID, topic)
		}
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

func (portal *Portal) UpdateMetadata(user *User, groupInfo *types.GroupInfo) bool {
	if portal.IsPrivateChat() {
		return false
	} else if portal.IsStatusBroadcastList() {
		update := false
		update = portal.UpdateName(StatusBroadcastName, types.EmptyJID, false) || update
		update = portal.UpdateTopic(StatusBroadcastTopic, types.EmptyJID, false) || update
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
	if groupInfo == nil {
		var err error
		groupInfo, err = user.Client.GetGroupInfo(portal.Key.JID)
		if err != nil {
			portal.log.Errorln("Failed to get group info:", err)
			return false
		}
	}

	portal.SyncParticipants(user, groupInfo)
	update := false
	update = portal.UpdateName(groupInfo.Name, groupInfo.NameSetBy, false) || update
	update = portal.UpdateTopic(groupInfo.Topic, groupInfo.TopicSetBy, false) || update
	if portal.ExpirationTime != groupInfo.DisappearingTimer {
		update = true
		portal.ExpirationTime = groupInfo.DisappearingTimer
	}

	portal.RestrictMessageSending(groupInfo.IsAnnounce)
	portal.RestrictMetadataChanges(groupInfo.IsLocked)

	return update
}

func (portal *Portal) ensureMXIDInvited(mxid id.UserID) {
	err := portal.MainIntent().EnsureInvited(portal.MXID, mxid)
	if err != nil {
		portal.log.Warnfln("Failed to ensure %s is invited to %s: %v", mxid, portal.MXID, err)
	}
}

func (portal *Portal) ensureUserInvited(user *User) bool {
	return user.ensureInvited(portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) UpdateMatrixRoom(user *User, groupInfo *types.GroupInfo) bool {
	if len(portal.MXID) == 0 {
		return false
	}
	portal.log.Infoln("Syncing portal for", user.MXID)

	portal.ensureUserInvited(user)
	go portal.addToSpace(user)

	update := false
	update = portal.UpdateMetadata(user, groupInfo) || update
	if !portal.IsPrivateChat() && !portal.IsBroadcastList() && portal.Avatar == "" {
		update = portal.UpdateAvatar(user, types.EmptyJID, false) || update
	}
	if update {
		portal.Update(nil)
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
			event.EventReaction.Type:   anyone,
			event.EventRedaction.Type:  anyone,
		},
	}
}

func (portal *Portal) applyPowerLevelFixes(levels *event.PowerLevelsEventContent) bool {
	changed := false
	changed = levels.EnsureEventLevel(event.EventReaction, 0) || changed
	changed = levels.EnsureEventLevel(event.EventRedaction, 0) || changed
	return changed
}

func (portal *Portal) ChangeAdminStatus(jids []types.JID, setAdmin bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if setAdmin {
		newLevel = 50
	}
	changed := portal.applyPowerLevelFixes(levels)
	for _, jid := range jids {
		puppet := portal.bridge.GetPuppetByJID(jid)
		changed = levels.EnsureUserLevel(puppet.MXID, newLevel) || changed

		user := portal.bridge.GetUserByJID(jid)
		if user != nil {
			changed = levels.EnsureUserLevel(user.MXID, newLevel) || changed
		}
	}
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

func (portal *Portal) RestrictMessageSending(restrict bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}

	newLevel := 0
	if restrict {
		newLevel = 50
	}

	changed := portal.applyPowerLevelFixes(levels)
	if levels.EventsDefault == newLevel && !changed {
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
	changed := portal.applyPowerLevelFixes(levels)
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

func (portal *Portal) getBridgeInfo() (string, event.BridgeEventContent) {
	bridgeInfo := event.BridgeEventContent{
		BridgeBot: portal.bridge.Bot.UserID,
		Creator:   portal.MainIntent().UserID,
		Protocol: event.BridgeInfoSection{
			ID:          "whatsapp",
			DisplayName: "WhatsApp",
			AvatarURL:   portal.bridge.Config.AppService.Bot.ParsedAvatar.CUString(),
			ExternalURL: "https://www.whatsapp.com/",
		},
		Channel: event.BridgeInfoSection{
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
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update m.bridge:", err)
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		portal.log.Warnln("Failed to update uk.half-shot.bridge:", err)
	}
}

func (portal *Portal) CreateMatrixRoom(user *User, groupInfo *types.GroupInfo, isFullInfo, backfill bool) error {
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

	//var broadcastMetadata *types.BroadcastListInfo
	if portal.IsPrivateChat() {
		puppet := portal.bridge.GetPuppetByJID(portal.Key.JID)
		puppet.SyncContact(user, true, false, "creating private chat portal")
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
		if groupInfo == nil || !isFullInfo {
			foundInfo, err := user.Client.GetGroupInfo(portal.Key.JID)

			// Ensure that the user is actually a participant in the conversation
			// before creating the matrix room
			if errors.Is(err, whatsmeow.ErrNotInGroup) {
				user.log.Debugfln("Skipping creating matrix room for %s because the user is not a participant", portal.Key.JID)
				user.bridge.DB.Backfill.DeleteAllForPortal(user.MXID, portal.Key)
				user.bridge.DB.HistorySync.DeleteAllMessagesForPortal(user.MXID, portal.Key)
				return err
			} else if err != nil {
				portal.log.Warnfln("Failed to get group info through %s: %v", user.JID, err)
			} else {
				groupInfo = foundInfo
				isFullInfo = true
			}
		}
		if groupInfo != nil {
			portal.Name = groupInfo.Name
			portal.Topic = groupInfo.Topic
		}
		portal.UpdateAvatar(user, types.EmptyJID, false)
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()

	initialState := []*event.Event{{
		Type: event.StatePowerLevels,
		Content: event.Content{
			Parsed: portal.GetBasePowerLevels(),
		},
	}, {
		Type:     event.StateBridge,
		Content:  event.Content{Parsed: bridgeInfo},
		StateKey: &bridgeInfoStateKey,
	}, {
		// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
		Type:     event.StateHalfShotBridge,
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

	creationContent := make(map[string]interface{})
	if !portal.bridge.Config.Bridge.FederateRooms {
		creationContent["m.federate"] = false
	}
	resp, err := intent.CreateRoom(&mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            portal.Name,
		Topic:           portal.Topic,
		Invite:          invite,
		Preset:          "private_chat",
		IsDirect:        portal.IsPrivateChat(),
		InitialState:    initialState,
		CreationContent: creationContent,
	})
	if err != nil {
		return err
	}
	portal.MXID = resp.RoomID
	portal.Update(nil)
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()

	// We set the memberships beforehand to make sure the encryption key exchange in initial backfill knows the users are here.
	for _, userID := range invite {
		portal.bridge.StateStore.SetMembership(portal.MXID, userID, event.MembershipInvite)
	}

	portal.ensureUserInvited(user)
	user.syncChatDoublePuppetDetails(portal, true)

	go portal.addToSpace(user)

	if groupInfo != nil {
		if groupInfo.IsEphemeral {
			portal.ExpirationTime = groupInfo.DisappearingTimer
			portal.Update(nil)
		}
		portal.SyncParticipants(user, groupInfo)
		if groupInfo.IsAnnounce {
			portal.RestrictMessageSending(groupInfo.IsAnnounce)
		}
		if groupInfo.IsLocked {
			portal.RestrictMetadataChanges(groupInfo.IsLocked)
		}
	}
	//if broadcastMetadata != nil {
	//	portal.SyncBroadcastRecipients(user, broadcastMetadata)
	//}
	if portal.IsPrivateChat() {
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
		portal.Update(nil)
	}

	if user.bridge.Config.Bridge.HistorySync.Backfill && backfill {
		portals := []*Portal{portal}
		user.EnqueueImmedateBackfills(portals)
		user.EnqueueDeferredBackfills(portals)
		user.BackfillQueue.ReCheck()
	}
	return nil
}

func (portal *Portal) addToSpace(user *User) {
	spaceID := user.GetSpaceRoom()
	if len(spaceID) == 0 || user.IsInSpace(portal.Key) {
		return
	}
	_, err := portal.bridge.Bot.SendStateEvent(spaceID, event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{portal.bridge.Config.Homeserver.Domain},
	})
	if err != nil {
		portal.log.Errorfln("Failed to add room to %s's personal filtering space (%s): %v", user.MXID, spaceID, err)
	} else {
		portal.log.Debugfln("Added room to %s's personal filtering space (%s)", user.MXID, spaceID)
		user.MarkInSpace(portal.Key)
	}
}

func (portal *Portal) IsPrivateChat() bool {
	return portal.Key.JID.Server == types.DefaultUserServer
}

func (portal *Portal) IsGroupChat() bool {
	return portal.Key.JID.Server == types.GroupServer
}

func (portal *Portal) IsBroadcastList() bool {
	return portal.Key.JID.Server == types.BroadcastServer
}

func (portal *Portal) IsStatusBroadcastList() bool {
	return portal.Key.JID == types.StatusBroadcastJID
}

func (portal *Portal) HasRelaybot() bool {
	return portal.bridge.Config.Bridge.Relay.Enabled && len(portal.RelayUserID) > 0
}

func (portal *Portal) GetRelayUser() *User {
	if !portal.HasRelaybot() {
		return nil
	} else if portal.relayUser == nil {
		portal.relayUser = portal.bridge.GetUserByMXID(portal.RelayUserID)
	}
	return portal.relayUser
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.bridge.GetPuppetByJID(portal.Key.JID).DefaultIntent()
	}
	return portal.bridge.Bot
}

func (portal *Portal) SetReply(content *event.MessageEventContent, replyToID types.MessageID) bool {
	if len(replyToID) == 0 {
		return false
	}
	message := portal.bridge.DB.Message.GetByJID(portal.Key, replyToID)
	if message == nil || message.IsFakeMXID() {
		return false
	}
	evt, err := portal.MainIntent().GetEvent(portal.MXID, message.MXID)
	if err != nil {
		portal.log.Warnln("Failed to get reply target:", err)
		content.RelatesTo = &event.RelatesTo{
			EventID: message.MXID,
			Type:    event.RelReply,
		}
		return true
	}
	_ = evt.Content.ParseRaw(evt.Type)
	if evt.Type == event.EventEncrypted {
		decryptedEvt, err := portal.bridge.Crypto.Decrypt(evt)
		if err != nil {
			portal.log.Warnln("Failed to decrypt reply target:", err)
		} else {
			evt = decryptedEvt
		}
	}
	content.SetReply(evt)
	return true
}

type sendReactionContent struct {
	event.ReactionEventContent
	DoublePuppet string `json:"fi.mau.double_puppet_source,omitempty"`
}

func (portal *Portal) HandleMessageReaction(intent *appservice.IntentAPI, user *User, info *types.MessageInfo, reaction *waProto.ReactionMessage, existingMsg *database.Message) {
	if existingMsg != nil {
		_, _ = portal.MainIntent().RedactEvent(portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
			Reason: "The undecryptable message was actually a reaction",
		})
	}

	targetJID := reaction.GetKey().GetId()
	if reaction.GetText() == "" {
		existing := portal.bridge.DB.Reaction.GetByTargetJID(portal.Key, targetJID, info.Sender)
		if existing == nil {
			portal.log.Debugfln("Dropping removal %s of unknown reaction to %s from %s", info.ID, targetJID, info.Sender)
			return
		}

		extra := make(map[string]interface{})
		if intent.IsCustomPuppet {
			extra[doublePuppetKey] = doublePuppetValue
		}
		resp, err := intent.RedactEvent(portal.MXID, existing.MXID, mautrix.ReqRedact{Extra: extra})
		if err != nil {
			portal.log.Errorfln("Failed to redact reaction %s/%s from %s to %s: %v", existing.MXID, existing.JID, info.Sender, targetJID, err)
		}
		portal.finishHandling(existingMsg, info, resp.EventID, database.MsgReaction, database.MsgNoError)
		existing.Delete()
	} else {
		target := portal.bridge.DB.Message.GetByJID(portal.Key, targetJID)
		if target == nil {
			portal.log.Debugfln("Dropping reaction %s from %s to unknown message %s", info.ID, info.Sender, targetJID)
			return
		}

		var content sendReactionContent
		content.RelatesTo = event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: target.MXID,
			Key:     variationselector.Add(reaction.GetText()),
		}
		if intent.IsCustomPuppet {
			content.DoublePuppet = doublePuppetValue
		}
		resp, err := intent.SendMassagedMessageEvent(portal.MXID, event.EventReaction, &content, info.Timestamp.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Failed to bridge reaction %s from %s to %s: %v", info.ID, info.Sender, target.JID, err)
			return
		}

		portal.finishHandling(existingMsg, info, resp.EventID, database.MsgReaction, database.MsgNoError)
		portal.upsertReaction(intent, target.JID, info.Sender, resp.EventID, info.ID)
	}
}

func (portal *Portal) HandleMessageRevoke(user *User, info *types.MessageInfo, key *waProto.MessageKey) bool {
	msg := portal.bridge.DB.Message.GetByJID(portal.Key, key.GetId())
	if msg == nil || msg.IsFakeMXID() {
		return false
	}
	intent := portal.bridge.GetPuppetByJID(info.Sender).IntentFor(portal)
	redactionReq := mautrix.ReqRedact{Extra: map[string]interface{}{}}
	if intent.IsCustomPuppet {
		redactionReq.Extra[doublePuppetKey] = doublePuppetValue
	}
	_, err := intent.RedactEvent(portal.MXID, msg.MXID, redactionReq)
	if err != nil {
		if errors.Is(err, mautrix.MForbidden) {
			_, err = portal.MainIntent().RedactEvent(portal.MXID, msg.MXID, redactionReq)
			if err != nil {
				portal.log.Errorln("Failed to redact %s: %v", msg.JID, err)
			}
		}
	} else {
		msg.Delete()
	}
	return true
}

func (portal *Portal) sendMainIntentMessage(content *event.MessageEventContent) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(portal.MainIntent(), event.EventMessage, content, nil, 0)
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

const doublePuppetKey = "fi.mau.double_puppet_source"
const doublePuppetValue = "mautrix-whatsapp"

func (portal *Portal) sendMessage(intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	if timestamp != 0 && intent.IsCustomPuppet {
		if wrappedContent.Raw == nil {
			wrappedContent.Raw = map[string]interface{}{}
		}
		if intent.IsCustomPuppet {
			wrappedContent.Raw[doublePuppetKey] = doublePuppetValue
		}
	}
	var err error
	eventType, err = portal.encrypt(&wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	if eventType == event.EventEncrypted {
		// Clear other custom keys if the event was encrypted, but keep the double puppet identifier
		if intent.IsCustomPuppet {
			wrappedContent.Raw = map[string]interface{}{doublePuppetKey: doublePuppetValue}
		} else {
			wrappedContent.Raw = nil
		}
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
	Extra   map[string]interface{}
	Caption *event.MessageEventContent

	MultiEvent []*event.MessageEventContent

	ReplyTo   types.MessageID
	ExpiresIn uint32
	Error     database.MessageErrorType
	MediaKey  []byte
}

func (portal *Portal) convertTextMessage(intent *appservice.IntentAPI, source *User, msg *waProto.Message) *ConvertedMessage {
	content := &event.MessageEventContent{
		Body:    msg.GetConversation(),
		MsgType: event.MsgText,
	}
	if len(msg.GetExtendedTextMessage().GetText()) > 0 {
		content.Body = msg.GetExtendedTextMessage().GetText()
	}

	contextInfo := msg.GetExtendedTextMessage().GetContextInfo()
	portal.bridge.Formatter.ParseWhatsApp(portal.MXID, content, contextInfo.GetMentionedJid())
	replyTo := contextInfo.GetStanzaId()
	expiresIn := contextInfo.GetExpiration()
	extraAttrs := map[string]interface{}{}
	extraAttrs["com.beeper.linkpreviews"] = portal.convertURLPreviewToBeeper(intent, source, msg.GetExtendedTextMessage())

	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		ReplyTo:   replyTo,
		ExpiresIn: expiresIn,
		Extra:     extraAttrs,
	}
}

func (portal *Portal) convertLiveLocationMessage(intent *appservice.IntentAPI, msg *waProto.LiveLocationMessage) *ConvertedMessage {
	content := &event.MessageEventContent{
		Body:    "Started sharing live location",
		MsgType: event.MsgNotice,
	}
	if len(msg.GetCaption()) > 0 {
		content.Body += ": " + msg.GetCaption()
	}
	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		ReplyTo:   msg.GetContextInfo().GetStanzaId(),
		ExpiresIn: msg.GetContextInfo().GetExpiration(),
	}
}

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

	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		ReplyTo:   msg.GetContextInfo().GetStanzaId(),
		ExpiresIn: msg.GetContextInfo().GetExpiration(),
	}
}

const inviteMsg = `%s<hr/>This invitation to join "%s" expires at %s. Reply to this message with <code>!wa accept</code> to accept the invite.`
const inviteMetaField = "fi.mau.whatsapp.invite"
const escapedInviteMetaField = `fi\.mau\.whatsapp\.invite`

type InviteMeta struct {
	JID        types.JID `json:"jid"`
	Code       string    `json:"code"`
	Expiration int64     `json:"expiration,string"`
	Inviter    types.JID `json:"inviter"`
}

func (portal *Portal) convertGroupInviteMessage(intent *appservice.IntentAPI, info *types.MessageInfo, msg *waProto.GroupInviteMessage) *ConvertedMessage {
	expiry := time.Unix(msg.GetInviteExpiration(), 0)
	htmlMessage := fmt.Sprintf(inviteMsg, html.EscapeString(msg.GetCaption()), msg.GetGroupName(), expiry)
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          format.HTMLToText(htmlMessage),
		Format:        event.FormatHTML,
		FormattedBody: htmlMessage,
	}
	groupJID, err := types.ParseJID(msg.GetGroupJid())
	if err != nil {
		portal.log.Errorfln("Failed to parse invite group JID: %v", err)
	}
	extraAttrs := map[string]interface{}{
		inviteMetaField: InviteMeta{
			JID:        groupJID,
			Code:       msg.GetInviteCode(),
			Expiration: msg.GetInviteExpiration(),
			Inviter:    info.Sender.ToNonAD(),
		},
	}
	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		Extra:     extraAttrs,
		ReplyTo:   msg.GetContextInfo().GetStanzaId(),
		ExpiresIn: msg.GetContextInfo().GetExpiration(),
	}
}

func (portal *Portal) convertContactMessage(intent *appservice.IntentAPI, msg *waProto.ContactMessage) *ConvertedMessage {
	fileName := fmt.Sprintf("%s.vcf", msg.GetDisplayName())
	data := []byte(msg.GetVcard())
	mimeType := "text/vcard"
	uploadMimeType, file := portal.encryptFileInPlace(data, mimeType)

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

	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		ReplyTo:   msg.GetContextInfo().GetStanzaId(),
		ExpiresIn: msg.GetContextInfo().GetExpiration(),
	}
}

func (portal *Portal) convertContactsArrayMessage(intent *appservice.IntentAPI, msg *waProto.ContactsArrayMessage) *ConvertedMessage {
	name := msg.GetDisplayName()
	if len(name) == 0 {
		name = fmt.Sprintf("%d contacts", len(msg.GetContacts()))
	}
	contacts := make([]*event.MessageEventContent, 0, len(msg.GetContacts()))
	for _, contact := range msg.GetContacts() {
		converted := portal.convertContactMessage(intent, contact)
		if converted != nil {
			contacts = append(contacts, converted.Content)
		}
	}
	return &ConvertedMessage{
		Intent: intent,
		Type:   event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("Sent %s", name),
		},
		ReplyTo:    msg.GetContextInfo().GetStanzaId(),
		ExpiresIn:  msg.GetContextInfo().GetExpiration(),
		MultiEvent: contacts,
	}
}

func (portal *Portal) tryKickUser(userID id.UserID, intent *appservice.IntentAPI) error {
	_, err := intent.KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
	if err != nil {
		httpErr, ok := err.(mautrix.HTTPError)
		if ok && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_FORBIDDEN" {
			_, err = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: userID})
		}
	}
	return err
}

func (portal *Portal) removeUser(isSameUser bool, kicker *appservice.IntentAPI, target id.UserID, targetIntent *appservice.IntentAPI) {
	if !isSameUser || targetIntent == nil {
		err := portal.tryKickUser(target, kicker)
		if err != nil {
			portal.log.Warnfln("Failed to kick %s from %s: %v", target, portal.MXID, err)
			if targetIntent != nil {
				_, _ = portal.leaveWithPuppetMeta(targetIntent)
			}
		}
	} else {
		_, err := portal.leaveWithPuppetMeta(targetIntent)
		if err != nil {
			portal.log.Warnfln("Failed to leave portal as %s: %v", target, err)
			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: target})
		}
	}
}

func (portal *Portal) HandleWhatsAppKick(source *User, senderJID types.JID, jids []types.JID) {
	sender := portal.bridge.GetPuppetByJID(senderJID)
	senderIntent := sender.IntentFor(portal)
	for _, jid := range jids {
		//if source != nil && source.JID.User == jid.User {
		//	portal.log.Debugln("Ignoring self-kick by", source.MXID)
		//	continue
		//}
		puppet := portal.bridge.GetPuppetByJID(jid)
		portal.removeUser(puppet.JID == sender.JID, senderIntent, puppet.MXID, puppet.DefaultIntent())

		if !portal.IsBroadcastList() {
			user := portal.bridge.GetUserByJID(jid)
			if user != nil {
				var customIntent *appservice.IntentAPI
				if puppet.CustomMXID == user.MXID {
					customIntent = puppet.CustomIntent()
				}
				portal.removeUser(puppet.JID == sender.JID, senderIntent, user.MXID, customIntent)
			}
		}
	}
}

func (portal *Portal) leaveWithPuppetMeta(intent *appservice.IntentAPI) (*mautrix.RespSendEvent, error) {
	content := event.Content{
		Parsed: event.MemberEventContent{
			Membership: event.MembershipLeave,
		},
		Raw: map[string]interface{}{
			doublePuppetKey: doublePuppetValue,
		},
	}
	// Bypass IntentAPI, we don't want to EnsureJoined here
	return intent.Client.SendStateEvent(portal.MXID, event.StateMember, intent.UserID.String(), &content)
}

func (portal *Portal) HandleWhatsAppInvite(source *User, senderJID *types.JID, jids []types.JID) (evtID id.EventID) {
	intent := portal.MainIntent()
	if senderJID != nil && !senderJID.IsEmpty() {
		sender := portal.bridge.GetPuppetByJID(*senderJID)
		intent = sender.IntentFor(portal)
	}
	for _, jid := range jids {
		puppet := portal.bridge.GetPuppetByJID(jid)
		puppet.SyncContact(source, true, false, "handling whatsapp invite")
		content := event.Content{
			Parsed: event.MemberEventContent{
				Membership:  "invite",
				Displayname: puppet.Displayname,
				AvatarURL:   puppet.AvatarURL.CUString(),
			},
			Raw: map[string]interface{}{
				doublePuppetKey: doublePuppetValue,
			},
		}
		resp, err := intent.SendStateEvent(portal.MXID, event.StateMember, puppet.MXID.String(), &content)
		if err != nil {
			portal.log.Warnfln("Failed to invite %s as %s: %v", puppet.MXID, intent.UserID, err)
			_ = portal.MainIntent().EnsureInvited(portal.MXID, puppet.MXID)
		} else {
			evtID = resp.EventID
		}
		err = puppet.DefaultIntent().EnsureJoined(portal.MXID)
		if err != nil {
			portal.log.Errorfln("Failed to ensure %s is joined: %v", puppet.MXID, err)
		}
	}
	return
}

const failedMediaField = "fi.mau.whatsapp.failed_media"

type FailedMediaKeys struct {
	Key       []byte              `json:"key"`
	Length    int                 `json:"length"`
	Type      whatsmeow.MediaType `json:"type"`
	SHA256    []byte              `json:"sha256"`
	EncSHA256 []byte              `json:"enc_sha256"`
}

type FailedMediaMeta struct {
	Type         event.Type                 `json:"type"`
	Content      *event.MessageEventContent `json:"content"`
	ExtraContent map[string]interface{}     `json:"extra_content,omitempty"`
	Media        FailedMediaKeys            `json:"whatsapp_media"`
}

func shallowCopyMap(data map[string]interface{}) map[string]interface{} {
	newMap := make(map[string]interface{}, len(data))
	for key, value := range data {
		newMap[key] = value
	}
	return newMap
}

func (portal *Portal) makeMediaBridgeFailureMessage(info *types.MessageInfo, bridgeErr error, converted *ConvertedMessage, keys *FailedMediaKeys, userFriendlyError string) *ConvertedMessage {
	if errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith410) {
		portal.log.Debugfln("Failed to bridge media for %s: %v", info.ID, bridgeErr)
	} else {
		portal.log.Errorfln("Failed to bridge media for %s: %v", info.ID, bridgeErr)
	}
	if keys != nil {
		meta := &FailedMediaMeta{
			Type:         converted.Type,
			Content:      converted.Content,
			ExtraContent: shallowCopyMap(converted.Extra),
			Media:        *keys,
		}
		converted.Extra[failedMediaField] = meta
		portal.mediaErrorCache[info.ID] = meta
	}
	converted.Type = event.EventMessage
	body := userFriendlyError
	if body == "" {
		body = fmt.Sprintf("Failed to bridge media: %v", bridgeErr)
	}
	converted.Content = &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    body,
	}
	return converted
}

func (portal *Portal) encryptFileInPlace(data []byte, mimeType string) (string, *event.EncryptedFileInfo) {
	if !portal.Encrypted {
		return mimeType, nil
	}

	file := &event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	file.EncryptInPlace(data)
	return "application/octet-stream", file
}

type MediaMessage interface {
	whatsmeow.DownloadableMessage
	GetContextInfo() *waProto.ContextInfo
	GetFileLength() uint64
	GetMimetype() string
}

type MediaMessageWithThumbnail interface {
	MediaMessage
	GetJpegThumbnail() []byte
}

type MediaMessageWithCaption interface {
	MediaMessage
	GetCaption() string
}

type MediaMessageWithDimensions interface {
	MediaMessage
	GetHeight() uint32
	GetWidth() uint32
}

type MediaMessageWithFileName interface {
	MediaMessage
	GetFileName() string
}

type MediaMessageWithDuration interface {
	MediaMessage
	GetSeconds() uint32
}

func (portal *Portal) convertMediaMessageContent(intent *appservice.IntentAPI, msg MediaMessage) *ConvertedMessage {
	content := &event.MessageEventContent{
		Info: &event.FileInfo{
			MimeType: msg.GetMimetype(),
			Size:     int(msg.GetFileLength()),
		},
	}
	extraContent := map[string]interface{}{}

	messageWithDimensions, ok := msg.(MediaMessageWithDimensions)
	if ok {
		content.Info.Width = int(messageWithDimensions.GetWidth())
		content.Info.Height = int(messageWithDimensions.GetHeight())
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

		content.Body += util.ExtensionFromMimetype(msg.GetMimetype())
	}

	msgWithDuration, ok := msg.(MediaMessageWithDuration)
	if ok {
		content.Info.Duration = int(msgWithDuration.GetSeconds()) * 1000
	}

	videoMessage, ok := msg.(*waProto.VideoMessage)
	var isGIF bool
	if ok && videoMessage.GetGifPlayback() {
		isGIF = true
		extraContent["info"] = map[string]interface{}{
			"fi.mau.loop":          true,
			"fi.mau.autoplay":      true,
			"fi.mau.hide_controls": true,
			"fi.mau.no_audio":      true,
		}
	}

	messageWithThumbnail, ok := msg.(MediaMessageWithThumbnail)
	if ok && messageWithThumbnail.GetJpegThumbnail() != nil && (portal.bridge.Config.Bridge.WhatsappThumbnail || isGIF) {
		thumbnailData := messageWithThumbnail.GetJpegThumbnail()
		thumbnailMime := http.DetectContentType(thumbnailData)
		thumbnailCfg, _, _ := image.DecodeConfig(bytes.NewReader(thumbnailData))
		thumbnailSize := len(thumbnailData)
		thumbnailUploadMime, thumbnailFile := portal.encryptFileInPlace(thumbnailData, thumbnailMime)
		uploadedThumbnail, err := intent.UploadBytes(thumbnailData, thumbnailUploadMime)
		if err != nil {
			portal.log.Warnfln("Failed to upload thumbnail: %v", err)
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

	audioMessage, ok := msg.(*waProto.AudioMessage)
	if ok {
		var waveform []int
		if audioMessage.Waveform != nil {
			waveform = make([]int, len(audioMessage.Waveform))
			max := 0
			for i, part := range audioMessage.Waveform {
				waveform[i] = int(part)
				if waveform[i] > max {
					max = waveform[i]
				}
			}
			multiplier := 0
			if max > 0 {
				multiplier = 1024 / max
			}
			if multiplier > 32 {
				multiplier = 32
			}
			for i := range waveform {
				waveform[i] *= multiplier
			}
		}
		extraContent["org.matrix.msc1767.audio"] = map[string]interface{}{
			"duration": int(audioMessage.GetSeconds()) * 1000,
			"waveform": waveform,
		}
		if audioMessage.GetPtt() {
			extraContent["org.matrix.msc3245.voice"] = map[string]interface{}{}
		}
	}

	messageWithCaption, ok := msg.(MediaMessageWithCaption)
	var captionContent *event.MessageEventContent
	if ok && len(messageWithCaption.GetCaption()) > 0 {
		captionContent = &event.MessageEventContent{
			Body:    messageWithCaption.GetCaption(),
			MsgType: event.MsgNotice,
		}

		portal.bridge.Formatter.ParseWhatsApp(portal.MXID, captionContent, msg.GetContextInfo().GetMentionedJid())
	}

	return &ConvertedMessage{
		Intent:    intent,
		Type:      eventType,
		Content:   content,
		Caption:   captionContent,
		ReplyTo:   msg.GetContextInfo().GetStanzaId(),
		ExpiresIn: msg.GetContextInfo().GetExpiration(),
		Extra:     extraContent,
	}
}

func (portal *Portal) uploadMedia(intent *appservice.IntentAPI, data []byte, content *event.MessageEventContent) error {
	uploadMimeType, file := portal.encryptFileInPlace(data, content.Info.MimeType)

	req := mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  uploadMimeType,
	}
	var mxc id.ContentURI
	if portal.bridge.Config.Homeserver.AsyncMedia {
		uploaded, err := intent.UnstableUploadAsync(req)
		if err != nil {
			return err
		}
		mxc = uploaded.ContentURI
	} else {
		uploaded, err := intent.UploadMedia(req)
		if err != nil {
			return err
		}
		mxc = uploaded.ContentURI
	}

	if file != nil {
		file.URL = mxc.CUString()
		content.File = file
	} else {
		content.URL = mxc.CUString()
	}

	content.Info.Size = len(data)
	if content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width, content.Info.Height = cfg.Width, cfg.Height
	}
	return nil
}

func (portal *Portal) convertMediaMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, msg MediaMessage, typeName string, isBackfill bool) *ConvertedMessage {
	converted := portal.convertMediaMessageContent(intent, msg)
	data, err := source.Client.Download(msg)
	if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
		converted.Error = database.MsgErrMediaNotFound
		converted.MediaKey = msg.GetMediaKey()

		errorText := fmt.Sprintf("Old %s.", typeName)
		if portal.bridge.Config.Bridge.HistorySync.MediaRequests.AutoRequestMedia && isBackfill {
			errorText += " Media will be automatically requested from your phone later."
		} else {
			errorText += ` React with the \u267b (recycle) emoji to request this media from your phone.`
		}

		return portal.makeMediaBridgeFailureMessage(info, err, converted, &FailedMediaKeys{
			Key:       msg.GetMediaKey(),
			Length:    int(msg.GetFileLength()),
			Type:      whatsmeow.GetMediaType(msg),
			SHA256:    msg.GetFileSha256(),
			EncSHA256: msg.GetFileEncSha256(),
		}, errorText)
	} else if errors.Is(err, whatsmeow.ErrNoURLPresent) {
		portal.log.Debugfln("No URL present error for media message %s, ignoring...", info.ID)
		return nil
	} else if errors.Is(err, whatsmeow.ErrFileLengthMismatch) || errors.Is(err, whatsmeow.ErrInvalidMediaSHA256) {
		portal.log.Warnfln("Mismatching media checksums in %s: %v. Ignoring because WhatsApp seems to ignore them too", info.ID, err)
	} else if err != nil {
		return portal.makeMediaBridgeFailureMessage(info, err, converted, nil, "")
	}

	err = portal.uploadMedia(intent, data, converted.Content)
	if err != nil {
		if errors.Is(err, mautrix.MTooLarge) {
			return portal.makeMediaBridgeFailureMessage(info, errors.New("homeserver rejected too large file"), converted, nil, "")
		} else if httpErr, ok := err.(mautrix.HTTPError); ok && httpErr.IsStatus(413) {
			return portal.makeMediaBridgeFailureMessage(info, errors.New("proxy rejected too large file"), converted, nil, "")
		} else {
			return portal.makeMediaBridgeFailureMessage(info, fmt.Errorf("failed to upload media: %w", err), converted, nil, "")
		}
	}
	return converted
}

func (portal *Portal) fetchMediaRetryEvent(msg *database.Message) (*FailedMediaMeta, error) {
	errorMeta, ok := portal.mediaErrorCache[msg.JID]
	if ok {
		return errorMeta, nil
	}
	evt, err := portal.MainIntent().GetEvent(portal.MXID, msg.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch event %s: %w", msg.MXID, err)
	}
	if evt.Type == event.EventEncrypted {
		err = evt.Content.ParseRaw(evt.Type)
		if err != nil {
			return nil, fmt.Errorf("failed to parse encrypted content in %s: %w", msg.MXID, err)
		}
		evt, err = portal.bridge.Crypto.Decrypt(evt)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt event %s: %w", msg.MXID, err)
		}
	}
	errorMetaResult := gjson.GetBytes(evt.Content.VeryRaw, strings.ReplaceAll(failedMediaField, ".", "\\."))
	if !errorMetaResult.Exists() || !errorMetaResult.IsObject() {
		return nil, fmt.Errorf("didn't find failed media metadata in %s", msg.MXID)
	}
	var errorMetaBytes []byte
	if errorMetaResult.Index > 0 {
		errorMetaBytes = evt.Content.VeryRaw[errorMetaResult.Index : errorMetaResult.Index+len(errorMetaResult.Raw)]
	} else {
		errorMetaBytes = []byte(errorMetaResult.Raw)
	}
	err = json.Unmarshal(errorMetaBytes, &errorMeta)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal failed media metadata in %s: %w", msg.MXID, err)
	}
	return errorMeta, nil
}

func (portal *Portal) sendMediaRetryFailureEdit(intent *appservice.IntentAPI, msg *database.Message, err error) {
	content := event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("Failed to bridge media after re-requesting it from your phone: %v", err),
	}
	contentCopy := content
	content.NewContent = &contentCopy
	content.RelatesTo = &event.RelatesTo{
		EventID: msg.MXID,
		Type:    event.RelReplace,
	}
	resp, sendErr := portal.sendMessage(intent, event.EventMessage, &content, nil, time.Now().UnixMilli())
	if sendErr != nil {
		portal.log.Warnfln("Failed to edit %s after retry failure for %s: %v", msg.MXID, msg.JID, sendErr)
	} else {
		portal.log.Debugfln("Successfully edited %s -> %s after retry failure for %s", msg.MXID, resp.EventID, msg.JID)
	}

}

func (portal *Portal) handleMediaRetry(retry *events.MediaRetry, source *User) {
	msg := portal.bridge.DB.Message.GetByJID(portal.Key, retry.MessageID)
	if msg == nil {
		portal.log.Warnfln("Dropping media retry notification for unknown message %s", retry.MessageID)
		return
	} else if msg.Error != database.MsgErrMediaNotFound {
		portal.log.Warnfln("Dropping media retry notification for non-errored message %s / %s", retry.MessageID, msg.MXID)
		return
	}

	meta, err := portal.fetchMediaRetryEvent(msg)
	if err != nil {
		portal.log.Warnfln("Can't handle media retry notification for %s: %v", retry.MessageID, err)
		return
	}

	var puppet *Puppet
	if retry.FromMe {
		puppet = portal.bridge.GetPuppetByJID(source.JID)
	} else if retry.ChatID.Server == types.DefaultUserServer {
		puppet = portal.bridge.GetPuppetByJID(retry.ChatID)
	} else {
		puppet = portal.bridge.GetPuppetByJID(retry.SenderID)
	}
	intent := puppet.IntentFor(portal)

	retryData, err := whatsmeow.DecryptMediaRetryNotification(retry, meta.Media.Key)
	if err != nil {
		portal.log.Warnfln("Failed to handle media retry notification for %s: %v", retry.MessageID, err)
		portal.sendMediaRetryFailureEdit(intent, msg, err)
		return
	} else if retryData.GetResult() != waProto.MediaRetryNotification_SUCCESS {
		errorName := waProto.MediaRetryNotification_MediaRetryNotificationResultType_name[int32(retryData.GetResult())]
		portal.log.Warnfln("Got error response in media retry notification for %s: %s", retry.MessageID, errorName)
		portal.log.Debugfln("Error response contents: %s / %s", retryData.GetStanzaId(), retryData.GetDirectPath())
		if retryData.GetResult() == waProto.MediaRetryNotification_NOT_FOUND {
			portal.sendMediaRetryFailureEdit(intent, msg, whatsmeow.ErrMediaNotAvailableOnPhone)
		} else {
			portal.sendMediaRetryFailureEdit(intent, msg, fmt.Errorf("phone sent error response: %s", errorName))
		}
		return
	}

	data, err := source.Client.DownloadMediaWithPath(retryData.GetDirectPath(), meta.Media.EncSHA256, meta.Media.SHA256, meta.Media.Key, meta.Media.Length, meta.Media.Type, "")
	if err != nil {
		portal.log.Warnfln("Failed to download media in %s after retry notification: %v", retry.MessageID, err)
		portal.sendMediaRetryFailureEdit(intent, msg, err)
		return
	}
	err = portal.uploadMedia(intent, data, meta.Content)
	if err != nil {
		portal.log.Warnfln("Failed to re-upload media for %s after retry notification: %v", retry.MessageID, err)
		portal.sendMediaRetryFailureEdit(intent, msg, fmt.Errorf("re-uploading media failed: %v", err))
		return
	}
	replaceContent := &event.MessageEventContent{
		MsgType:    meta.Content.MsgType,
		Body:       "* " + meta.Content.Body,
		NewContent: meta.Content,
		RelatesTo: &event.RelatesTo{
			EventID: msg.MXID,
			Type:    event.RelReplace,
		},
	}
	// Move the extra content into m.new_content too
	meta.ExtraContent = map[string]interface{}{
		"m.new_content": shallowCopyMap(meta.ExtraContent),
	}
	resp, err := portal.sendMessage(intent, meta.Type, replaceContent, meta.ExtraContent, time.Now().UnixMilli())
	if err != nil {
		portal.log.Warnfln("Failed to edit %s after retry notification for %s: %v", msg.MXID, retry.MessageID, err)
		return
	}
	portal.log.Debugfln("Successfully edited %s -> %s after retry notification for %s", msg.MXID, resp.EventID, retry.MessageID)
	msg.UpdateMXID(nil, resp.EventID, database.MsgNormal, database.MsgNoError)
}

func (portal *Portal) requestMediaRetry(user *User, eventID id.EventID, mediaKey []byte) (bool, error) {
	msg := portal.bridge.DB.Message.GetByMXID(eventID)
	if msg == nil {
		err := errors.New(fmt.Sprintf("%s requested a media retry for unknown event %s", user.MXID, eventID))
		portal.log.Debugfln(err.Error())
		return false, err
	} else if msg.Error != database.MsgErrMediaNotFound {
		err := errors.New(fmt.Sprintf("%s requested a media retry for non-errored event %s", user.MXID, eventID))
		portal.log.Debugfln(err.Error())
		return false, err
	}

	// If the media key is not provided, grab it from the event in Matrix
	if mediaKey == nil {
		evt, err := portal.fetchMediaRetryEvent(msg)
		if err != nil {
			portal.log.Warnfln("Can't send media retry request for %s: %v", msg.JID, err)
			return true, nil
		}
		mediaKey = evt.Media.Key
	}

	err := user.Client.SendMediaRetryReceipt(&types.MessageInfo{
		ID: msg.JID,
		MessageSource: types.MessageSource{
			IsFromMe: msg.Sender.User == user.JID.User,
			IsGroup:  !portal.IsPrivateChat(),
			Sender:   msg.Sender,
			Chat:     portal.Key.JID,
		},
	}, mediaKey)
	if err != nil {
		portal.log.Warnfln("Failed to send media retry request for %s: %v", msg.JID, err)
	} else {
		portal.log.Debugfln("Sent media retry request for %s", msg.JID)
	}
	return true, err
}

const thumbnailMaxSize = 72
const thumbnailMinSize = 24

func createJPEGThumbnailAndGetSize(source []byte) ([]byte, int, int, error) {
	src, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to decode thumbnail: %w", err)
	}
	imageBounds := src.Bounds()
	width, height := imageBounds.Max.X, imageBounds.Max.Y
	var img image.Image
	if width <= thumbnailMaxSize && height <= thumbnailMaxSize {
		// No need to resize
		img = src
	} else {
		if width == height {
			width = thumbnailMaxSize
			height = thumbnailMaxSize
		} else if width < height {
			width /= height / thumbnailMaxSize
			height = thumbnailMaxSize
		} else {
			height /= width / thumbnailMaxSize
			width = thumbnailMaxSize
		}
		if width < thumbnailMinSize {
			width = thumbnailMinSize
		}
		if height < thumbnailMinSize {
			height = thumbnailMinSize
		}
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.NearestNeighbor.Scale(dst, dst.Rect, src, src.Bounds(), draw.Over, nil)
		img = dst
	}

	var buf bytes.Buffer
	err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpeg.DefaultQuality})
	if err != nil {
		return nil, width, height, fmt.Errorf("failed to re-encode thumbnail: %w", err)
	}
	return buf.Bytes(), width, height, nil
}

func createJPEGThumbnail(source []byte) ([]byte, error) {
	data, _, _, err := createJPEGThumbnailAndGetSize(source)
	return data, err
}

func (portal *Portal) downloadThumbnail(original []byte, thumbnailURL id.ContentURIString, eventID id.EventID) ([]byte, error) {
	if len(thumbnailURL) == 0 {
		// just fall back to making thumbnail of original
	} else if mxc, err := thumbnailURL.Parse(); err != nil {
		portal.log.Warnfln("Malformed thumbnail URL in %s: %v (falling back to generating thumbnail from source)", eventID, err)
	} else if thumbnail, err := portal.MainIntent().DownloadBytes(mxc); err != nil {
		portal.log.Warnfln("Failed to download thumbnail in %s: %v (falling back to generating thumbnail from source)", eventID, err)
	} else {
		return createJPEGThumbnail(thumbnail)
	}
	return createJPEGThumbnail(original)
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
		err = file.DecryptInPlace(data)
		if err != nil {
			portal.log.Errorfln("Failed to decrypt media in %s: %v", eventID, err)
			return nil
		}
	}
	if mediaType == whatsmeow.MediaVideo && content.GetInfo().MimeType == "image/gif" {
		data, err = ffmpeg.ConvertBytes(data, ".mp4", []string{"-f", "gif"}, []string{
			"-pix_fmt", "yuv420p", "-c:v", "libx264", "-movflags", "+faststart",
			"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
		}, content.GetInfo().MimeType)
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

	// Audio doesn't have thumbnails
	var thumbnail []byte
	if mediaType != whatsmeow.MediaAudio {
		thumbnail, err = portal.downloadThumbnail(data, content.GetInfo().ThumbnailURL, eventID)
		// Ignore format errors for non-image files, we don't care about those thumbnails
		if err != nil && (!errors.Is(err, image.ErrFormat) || mediaType == whatsmeow.MediaImage) {
			portal.log.Errorfln("Failed to generate thumbnail for %s: %v", eventID, err)
		}
	}

	return &MediaUpload{
		UploadResponse: uploadResp,
		Caption:        caption,
		MentionedJIDs:  mentionedJIDs,
		Thumbnail:      thumbnail,
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

func (portal *Portal) addRelaybotFormat(sender *User, content *event.MessageEventContent) bool {
	member := portal.MainIntent().Member(portal.MXID, sender.MXID)
	if member == nil {
		member = &event.MemberEventContent{}
	}

	if content.Format != event.FormatHTML {
		content.FormattedBody = strings.Replace(html.EscapeString(content.Body), "\n", "<br/>", -1)
		content.Format = event.FormatHTML
	}
	data, err := portal.bridge.Config.Bridge.Relay.FormatMessage(content, sender.MXID, *member)
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

func getUnstableWaveform(content map[string]interface{}) []byte {
	audioInfo, ok := content["org.matrix.msc1767.audio"].(map[string]interface{})
	if !ok {
		return nil
	}
	waveform, ok := audioInfo["waveform"].([]interface{})
	if !ok {
		return nil
	}
	output := make([]byte, len(waveform))
	var val float64
	for i, part := range waveform {
		val, ok = part.(float64)
		if ok {
			output[i] = byte(val / 4)
		}
	}
	return output
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
		replyToMsg := portal.bridge.DB.Message.GetByMXID(replyToID)
		if replyToMsg != nil && !replyToMsg.IsFakeJID() && replyToMsg.Type == database.MsgNormal {
			ctxInfo.StanzaId = &replyToMsg.JID
			ctxInfo.Participant = proto.String(replyToMsg.Sender.ToNonAD().String())
			// Using blank content here seems to work fine on all official WhatsApp apps.
			//
			// We could probably invent a slightly more accurate version of the quoted message
			// by fetching the Matrix event and converting it to the WhatsApp format, but that's
			// a lot of work and this works fine.
			ctxInfo.QuotedMessage = &waProto.Message{Conversation: proto.String("")}
		}
	}
	if portal.ExpirationTime != 0 {
		ctxInfo.Expiration = proto.Uint32(portal.ExpirationTime)
	}
	relaybotFormatted := false
	if !sender.IsLoggedIn() || (portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User) {
		if !portal.HasRelaybot() {
			portal.log.Warnln("Ignoring message from", sender.MXID, "in chat with no relaybot (convertMatrixMessage)")
			return nil, sender
		}
		relaybotFormatted = portal.addRelaybotFormat(sender, content)
		sender = portal.GetRelayUser()
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
		msg.ExtendedTextMessage = &waProto.ExtendedTextMessage{
			Text:        &text,
			ContextInfo: &ctxInfo,
		}
		hasPreview := portal.convertURLPreviewToWhatsApp(sender, evt, msg.ExtendedTextMessage)
		if ctxInfo.StanzaId == nil && ctxInfo.MentionedJid == nil && ctxInfo.Expiration == nil && !hasPreview {
			// No need for extended message
			msg.ExtendedTextMessage = nil
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
		if isMSC3245Voice {
			msg.AudioMessage.Waveform = getUnstableWaveform(evt.Content.Raw)
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
			JpegThumbnail: media.Thumbnail,
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
	resp, err := portal.sendMainIntentMessage(&event.MessageEventContent{
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
	if !portal.canBridgeFrom(sender, "message") {
		return
	}
	portal.log.Debugfln("Received event %s from %s", evt.ID, evt.Sender)
	msg, sender := portal.convertMatrixMessage(sender, evt)
	if msg == nil {
		return
	}
	portal.MarkDisappearing(evt.ID, portal.ExpirationTime, true)
	info := portal.generateMessageInfo(sender)
	dbMsg := portal.markHandled(nil, nil, info, evt.ID, false, true, database.MsgNormal, database.MsgNoError)
	portal.log.Debugln("Sending event", evt.ID, "to WhatsApp", info.ID)
	ts, err := sender.Client.SendMessage(portal.Key.JID, info.ID, msg)
	if err != nil {
		portal.log.Errorfln("Error sending message: %v", err)
		portal.sendErrorMessage(err.Error(), true)
		status := appservice.StatusPermFailure
		if errors.Is(err, whatsmeow.ErrBroadcastListUnsupported) {
			status = appservice.StatusUnsupported
		}
		checkpoint := appservice.NewMessageSendCheckpoint(evt, appservice.StepRemote, status, 0)
		checkpoint.Info = err.Error()
		go checkpoint.Send(portal.bridge.AS)
	} else {
		portal.log.Debugfln("Handled Matrix event %s", evt.ID)
		portal.bridge.AS.SendMessageSendCheckpoint(evt, appservice.StepRemote, 0)
		portal.sendDeliveryReceipt(evt.ID)
		dbMsg.MarkSent(ts)
	}
}

func (portal *Portal) HandleMatrixReaction(sender *User, evt *event.Event) {
	portal.log.Debugfln("Received reaction event %s from %s", evt.ID, evt.Sender)
	err := portal.handleMatrixReaction(sender, evt)
	if err != nil {
		portal.log.Errorfln("Error sending reaction %s: %v", evt.ID, err)
		portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, err, true, 0)
	} else {
		portal.log.Debugfln("Handled Matrix reaction %s", evt.ID)
		portal.bridge.AS.SendMessageSendCheckpoint(evt, appservice.StepRemote, 0)
		portal.sendDeliveryReceipt(evt.ID)
	}
}

func (portal *Portal) handleMatrixReaction(sender *User, evt *event.Event) error {
	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok {
		return fmt.Errorf("unexpected parsed content type %T", evt.Content.Parsed)
	}
	target := portal.bridge.DB.Message.GetByMXID(content.RelatesTo.EventID)
	if target == nil || target.Type == database.MsgReaction {
		return fmt.Errorf("unknown target event %s", content.RelatesTo.EventID)
	}
	info := portal.generateMessageInfo(sender)
	dbMsg := portal.markHandled(nil, nil, info, evt.ID, false, true, database.MsgReaction, database.MsgNoError)
	portal.upsertReaction(nil, target.JID, sender.JID, evt.ID, info.ID)
	portal.log.Debugln("Sending reaction", evt.ID, "to WhatsApp", info.ID)
	ts, err := portal.sendReactionToWhatsApp(sender, info.ID, target, content.RelatesTo.Key, evt.Timestamp)
	if err != nil {
		dbMsg.MarkSent(ts)
	}
	return err
}

func (portal *Portal) sendReactionToWhatsApp(sender *User, id types.MessageID, target *database.Message, key string, timestamp int64) (time.Time, error) {
	var messageKeyParticipant *string
	if !portal.IsPrivateChat() {
		messageKeyParticipant = proto.String(target.Sender.ToNonAD().String())
	}
	key = variationselector.Remove(key)
	return sender.Client.SendMessage(portal.Key.JID, id, &waProto.Message{
		ReactionMessage: &waProto.ReactionMessage{
			Key: &waProto.MessageKey{
				RemoteJid:   proto.String(portal.Key.JID.String()),
				FromMe:      proto.Bool(target.Sender.User == sender.JID.User),
				Id:          proto.String(target.JID),
				Participant: messageKeyParticipant,
			},
			Text:              proto.String(key),
			SenderTimestampMs: proto.Int64(timestamp),
		},
	})
}

func (portal *Portal) upsertReaction(intent *appservice.IntentAPI, targetJID types.MessageID, senderJID types.JID, mxid id.EventID, jid types.MessageID) {
	dbReaction := portal.bridge.DB.Reaction.GetByTargetJID(portal.Key, targetJID, senderJID)
	if dbReaction == nil {
		dbReaction = portal.bridge.DB.Reaction.New()
		dbReaction.Chat = portal.Key
		dbReaction.TargetJID = targetJID
		dbReaction.Sender = senderJID
	} else {
		portal.log.Debugfln("Redacting old Matrix reaction %s after new one (%s) was sent", dbReaction.MXID, mxid)
		var err error
		if intent != nil {
			extra := make(map[string]interface{})
			if intent.IsCustomPuppet {
				extra[doublePuppetKey] = doublePuppetValue
			}
			_, err = intent.RedactEvent(portal.MXID, dbReaction.MXID, mautrix.ReqRedact{Extra: extra})
		}
		if intent == nil || errors.Is(err, mautrix.MForbidden) {
			_, err = portal.MainIntent().RedactEvent(portal.MXID, dbReaction.MXID)
		}
		if err != nil {
			portal.log.Warnfln("Failed to remove old reaction %s: %v", dbReaction.MXID, err)
		}
	}
	dbReaction.MXID = mxid
	dbReaction.JID = jid
	dbReaction.Upsert()
}

func (portal *Portal) HandleMatrixRedaction(sender *User, evt *event.Event) {
	if !portal.canBridgeFrom(sender, "redaction") {
		return
	}
	portal.log.Debugfln("Received redaction %s from %s", evt.ID, evt.Sender)

	senderLogIdentifier := sender.MXID
	if !sender.HasSession() {
		sender = portal.GetRelayUser()
		senderLogIdentifier += " (through relaybot)"
	}

	msg := portal.bridge.DB.Message.GetByMXID(evt.Redacts)
	if msg == nil {
		portal.log.Debugfln("Ignoring redaction %s of unknown event by %s", evt.ID, senderLogIdentifier)
		portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, errors.New("target not found"), true, 0)
		return
	} else if msg.IsFakeJID() {
		portal.log.Debugfln("Ignoring redaction %s of fake event by %s", evt.ID, senderLogIdentifier)
		portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, errors.New("target is a fake event"), true, 0)
		return
	} else if msg.Sender.User != sender.JID.User {
		portal.log.Debugfln("Ignoring redaction %s of %s/%s by %s: message was sent by someone else (%s, not %s)", evt.ID, msg.MXID, msg.JID, senderLogIdentifier, msg.Sender, sender.JID)
		portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, errors.New("message was sent by someone else"), true, 0)
		return
	}

	var err error
	if msg.Type == database.MsgReaction {
		if reaction := portal.bridge.DB.Reaction.GetByMXID(evt.Redacts); reaction == nil {
			portal.log.Debugfln("Ignoring redaction of reaction %s: reaction database entry not found", evt.ID)
			portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, errors.New("reaction database entry not found"), true, 0)
			return
		} else if reactionTarget := reaction.GetTarget(); reactionTarget == nil {
			portal.log.Debugfln("Ignoring redaction of reaction %s: reaction target message not found", evt.ID)
			portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, errors.New("reaction target message not found"), true, 0)
			return
		} else {
			portal.log.Debugfln("Sending redaction reaction %s of %s/%s to WhatsApp", evt.ID, msg.MXID, msg.JID)
			_, err = portal.sendReactionToWhatsApp(sender, "", reactionTarget, "", evt.Timestamp)
		}
	} else {
		portal.log.Debugfln("Sending redaction %s of %s/%s to WhatsApp", evt.ID, msg.MXID, msg.JID)
		_, err = sender.Client.RevokeMessage(portal.Key.JID, msg.JID)
	}
	if err != nil {
		portal.log.Errorfln("Error handling Matrix redaction %s: %v", evt.ID, err)
		portal.bridge.AS.SendErrorMessageSendCheckpoint(evt, appservice.StepRemote, err, true, 0)
	} else {
		portal.log.Debugfln("Handled Matrix redaction %s of %s", evt.ID, evt.Redacts)
		portal.bridge.AS.SendMessageSendCheckpoint(evt, appservice.StepRemote, 0)
		portal.sendDeliveryReceipt(evt.ID)
	}
}

func (portal *Portal) HandleMatrixReadReceipt(sender *User, eventID id.EventID, receiptTimestamp time.Time, isExplicit bool) {
	if !sender.IsLoggedIn() {
		if isExplicit {
			portal.log.Debugfln("Ignoring read receipt by %s: user is not connected to WhatsApp", sender.JID)
		}
		return
	}

	maxTimestamp := receiptTimestamp
	// Implicit read receipts don't have an event ID that's already bridged
	if isExplicit {
		if message := portal.bridge.DB.Message.GetByMXID(eventID); message != nil {
			maxTimestamp = message.Timestamp
		}
	}

	prevTimestamp := sender.GetLastReadTS(portal.Key)
	lastReadIsZero := false
	if prevTimestamp.IsZero() {
		prevTimestamp = maxTimestamp.Add(-2 * time.Second)
		lastReadIsZero = true
	}

	messages := portal.bridge.DB.Message.GetMessagesBetween(portal.Key, prevTimestamp, maxTimestamp)
	if len(messages) > 0 {
		sender.SetLastReadTS(portal.Key, messages[len(messages)-1].Timestamp)
	}
	groupedMessages := make(map[types.JID][]types.MessageID)
	for _, msg := range messages {
		var key types.JID
		if msg.IsFakeJID() || msg.Sender.User == sender.JID.User {
			// Don't send read receipts for own messages or fake messages
			continue
		} else if !portal.IsPrivateChat() {
			key = msg.Sender
		} else if !msg.BroadcastListJID.IsEmpty() {
			key = msg.BroadcastListJID
		} // else: blank key (participant field isn't needed in direct chat read receipts)
		groupedMessages[key] = append(groupedMessages[key], msg.JID)
	}
	// For explicit read receipts, log even if there are no targets. For implicit ones only log when there are targets
	if len(groupedMessages) > 0 || isExplicit {
		portal.log.Debugfln("Sending read receipts by %s (last read: %d, was zero: %t, explicit: %t): %v",
			sender.JID, prevTimestamp.Unix(), lastReadIsZero, isExplicit, groupedMessages)
	}
	for messageSender, ids := range groupedMessages {
		chatJID := portal.Key.JID
		if messageSender.Server == types.BroadcastServer {
			chatJID = messageSender
			messageSender = portal.Key.JID
		}
		err := sender.Client.MarkRead(ids, receiptTimestamp, chatJID, messageSender)
		if err != nil {
			portal.log.Warnfln("Failed to mark %v as read by %s: %v", ids, sender.JID, err)
		}
	}
	if isExplicit {
		portal.ScheduleDisappearing()
	}
}

func typingDiff(prev, new []id.UserID) (started, stopped []id.UserID) {
OuterNew:
	for _, userID := range new {
		for _, previousUserID := range prev {
			if userID == previousUserID {
				continue OuterNew
			}
		}
		started = append(started, userID)
	}
OuterPrev:
	for _, userID := range prev {
		for _, previousUserID := range new {
			if userID == previousUserID {
				continue OuterPrev
			}
		}
		stopped = append(stopped, userID)
	}
	return
}

func (portal *Portal) setTyping(userIDs []id.UserID, state types.ChatPresence) {
	for _, userID := range userIDs {
		user := portal.bridge.GetUserByMXIDIfExists(userID)
		if user == nil || !user.IsLoggedIn() {
			continue
		}
		portal.log.Debugfln("Bridging typing change from %s to chat presence %s", state, user.MXID)
		err := user.Client.SendChatPresence(portal.Key.JID, state, types.ChatPresenceMediaText)
		if err != nil {
			portal.log.Warnln("Error sending chat presence:", err)
		}
		if portal.bridge.Config.Bridge.SendPresenceOnTyping {
			err = user.Client.SendPresence(types.PresenceAvailable)
			if err != nil {
				user.log.Warnln("Failed to set presence:", err)
			}
		}
	}
}

func (portal *Portal) HandleMatrixTyping(newTyping []id.UserID) {
	portal.currentlyTypingLock.Lock()
	defer portal.currentlyTypingLock.Unlock()
	startedTyping, stoppedTyping := typingDiff(portal.currentlyTyping, newTyping)
	portal.currentlyTyping = newTyping
	portal.setTyping(startedTyping, types.ChatPresenceComposing)
	portal.setTyping(stoppedTyping, types.ChatPresencePaused)
}

func (portal *Portal) canBridgeFrom(sender *User, evtType string) bool {
	if !sender.IsLoggedIn() {
		if portal.HasRelaybot() {
			return true
		} else if sender.Session != nil {
			portal.log.Debugfln("Ignoring %s from %s as user is not connected", evtType, sender.MXID)
			msg := format.RenderMarkdown(fmt.Sprintf("\u26a0 You are not connected to WhatsApp, so your %s was not bridged.", evtType), true, false)
			msg.MsgType = event.MsgNotice
			_, err := portal.sendMainIntentMessage(&msg)
			if err != nil {
				portal.log.Errorln("Failed to send bridging failure message:", err)
			}
		} else {
			portal.log.Debugfln("Ignoring %s from non-logged-in user %s in chat with no relay user", evtType, sender.MXID)
		}
		return false
	} else if portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User && !portal.HasRelaybot() {
		portal.log.Debugfln("Ignoring %s from different user %s/%s in private chat with no relay user", evtType, sender.MXID, sender.JID)
		return false
	}
	return true
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
		err := sender.Client.LeaveGroup(portal.Key.JID)
		if err != nil {
			portal.log.Errorfln("Failed to leave group as %s: %v", sender.MXID, err)
			return
		}
		//portal.log.Infoln("Leave response:", <-resp)
	}
	portal.CleanupIfEmpty()
}

func (portal *Portal) HandleMatrixKick(sender *User, target *Puppet) {
	_, err := sender.Client.UpdateGroupParticipants(portal.Key.JID, map[types.JID]whatsmeow.ParticipantChange{
		target.JID: whatsmeow.ParticipantChangeRemove,
	})
	if err != nil {
		portal.log.Errorfln("Failed to kick %s from group as %s: %v", target.JID, sender.MXID, err)
		return
	}
	//portal.log.Infoln("Kick %s response: %s", puppet.JID, <-resp)
}

func (portal *Portal) HandleMatrixInvite(sender *User, target *Puppet) {
	_, err := sender.Client.UpdateGroupParticipants(portal.Key.JID, map[types.JID]whatsmeow.ParticipantChange{
		target.JID: whatsmeow.ParticipantChangeAdd,
	})
	if err != nil {
		portal.log.Errorfln("Failed to add %s to group as %s: %v", target.JID, sender.MXID, err)
		return
	}
	//portal.log.Infofln("Add %s response: %s", puppet.JID, <-resp)
}

func (portal *Portal) HandleMatrixMeta(sender *User, evt *event.Event) {
	switch content := evt.Content.Parsed.(type) {
	case *event.RoomNameEventContent:
		if content.Name == portal.Name {
			return
		}
		portal.Name = content.Name
		err := sender.Client.SetGroupName(portal.Key.JID, content.Name)
		if err != nil {
			portal.log.Errorln("Failed to update group name:", err)
		}
	case *event.TopicEventContent:
		if content.Topic == portal.Topic {
			return
		}
		portal.Topic = content.Topic
		err := sender.Client.SetGroupTopic(portal.Key.JID, "", "", content.Topic)
		if err != nil {
			portal.log.Errorln("Failed to update group description:", err)
		}
	case *event.RoomAvatarEventContent:
		portal.avatarLock.Lock()
		defer portal.avatarLock.Unlock()
		if content.URL == portal.AvatarURL || (content.URL.IsEmpty() && portal.Avatar == "remove") {
			return
		}
		var data []byte
		var err error
		if !content.URL.IsEmpty() {
			data, err = portal.MainIntent().DownloadBytes(content.URL)
			if err != nil {
				portal.log.Errorfln("Failed to download updated avatar %s: %v", content.URL, err)
				return
			}
			portal.log.Debugfln("%s set the group avatar to %s", sender.MXID, content.URL)
		} else {
			portal.log.Debugfln("%s removed the group avatar", sender.MXID)
		}
		newID, err := sender.Client.SetGroupPhoto(portal.Key.JID, data)
		if err != nil {
			portal.log.Errorfln("Failed to update group avatar: %v", err)
			return
		}
		portal.log.Debugfln("Successfully updated group avatar to %s", newID)
		portal.Avatar = newID
		portal.AvatarURL = content.URL
		portal.UpdateBridgeInfo()
		portal.Update(nil)
	}
}
