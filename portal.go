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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"mime"
	"net/http"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chai2010/webp"
	"github.com/tidwall/gjson"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/proto"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util"
	"maunium.net/go/mautrix/util/dbutil"
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

func (br *WABridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByMXID[mxid]
	if !ok {
		return br.loadDBPortal(br.DB.Portal.GetByMXID(mxid), nil)
	}
	return portal
}

func (br *WABridge) GetIPortal(mxid id.RoomID) bridge.Portal {
	p := br.GetPortalByMXID(mxid)
	if p == nil {
		return nil
	}
	return p
}

func (portal *Portal) IsEncrypted() bool {
	return portal.Encrypted
}

func (portal *Portal) MarkEncrypted() {
	portal.Encrypted = true
	portal.Update(nil)
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser || portal.HasRelaybot() {
		portal.matrixMessages <- PortalMatrixMessage{user: user.(*User), evt: evt, receivedAt: time.Now()}
	}
}

func (br *WABridge) GetPortalByJID(key database.PortalKey) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByJID[key]
	if !ok {
		return br.loadDBPortal(br.DB.Portal.GetByJID(key), &key)
	}
	return portal
}

func (br *WABridge) GetExistingPortalByJID(key database.PortalKey) *Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByJID[key]
	if !ok {
		return br.loadDBPortal(br.DB.Portal.GetByJID(key), nil)
	}
	return portal
}

func (br *WABridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll())
}

func (br *WABridge) GetAllIPortals() (iportals []bridge.Portal) {
	portals := br.GetAllPortals()
	iportals = make([]bridge.Portal, len(portals))
	for i, portal := range portals {
		iportals[i] = portal
	}
	return iportals
}

func (br *WABridge) GetAllPortalsByJID(jid types.JID) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByJID(jid))
}

func (br *WABridge) GetAllByParentGroup(jid types.JID) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByParentGroup(jid))
}

func (br *WABridge) dbPortalsToPortals(dbPortals []*database.Portal) []*Portal {
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}
		portal, ok := br.portalsByJID[dbPortal.Key]
		if !ok {
			portal = br.loadDBPortal(dbPortal, nil)
		}
		output[index] = portal
	}
	return output
}

func (br *WABridge) loadDBPortal(dbPortal *database.Portal, key *database.PortalKey) *Portal {
	if dbPortal == nil {
		if key == nil {
			return nil
		}
		dbPortal = br.DB.Portal.New()
		dbPortal.Key = *key
		dbPortal.Insert()
	}
	portal := br.NewPortal(dbPortal)
	br.portalsByJID[portal.Key] = portal
	if len(portal.MXID) > 0 {
		br.portalsByMXID[portal.MXID] = portal
	}
	return portal
}

func (portal *Portal) GetUsers() []*User {
	return nil
}

func (br *WABridge) newBlankPortal(key database.PortalKey) *Portal {
	portal := &Portal{
		bridge: br,
		log:    br.Log.Sub(fmt.Sprintf("Portal/%s", key)),

		messages:       make(chan PortalMessage, br.Config.Bridge.PortalMessageBuffer),
		matrixMessages: make(chan PortalMatrixMessage, br.Config.Bridge.PortalMessageBuffer),
		mediaRetries:   make(chan PortalMediaRetry, br.Config.Bridge.PortalMessageBuffer),

		mediaErrorCache: make(map[types.MessageID]*FailedMediaMeta),
	}
	go portal.handleMessageLoop()
	return portal
}

func (br *WABridge) NewManualPortal(key database.PortalKey) *Portal {
	portal := br.newBlankPortal(key)
	portal.Portal = br.DB.Portal.New()
	portal.Key = key
	return portal
}

func (br *WABridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := br.newBlankPortal(dbPortal.Key)
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
	evt        *event.Event
	user       *User
	receivedAt time.Time
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

	bridge *WABridge
	log    log.Logger

	roomCreateLock sync.Mutex
	encryptLock    sync.Mutex
	backfillLock   sync.Mutex
	avatarLock     sync.Mutex

	latestEventBackfillLock sync.Mutex
	parentGroupUpdateLock   sync.Mutex

	recentlyHandled      [recentlyHandledLength]recentlyHandledWrapper
	recentlyHandledLock  sync.Mutex
	recentlyHandledIndex uint8

	currentlyTyping     []id.UserID
	currentlyTypingLock sync.Mutex

	messages       chan PortalMessage
	matrixMessages chan PortalMatrixMessage
	mediaRetries   chan PortalMediaRetry

	mediaErrorCache map[types.MessageID]*FailedMediaMeta

	currentlySleepingToDelete sync.Map

	relayUser    *User
	parentPortal *Portal
}

var (
	_ bridge.Portal                    = (*Portal)(nil)
	_ bridge.ReadReceiptHandlingPortal = (*Portal)(nil)
	_ bridge.MembershipHandlingPortal  = (*Portal)(nil)
	_ bridge.MetaHandlingPortal        = (*Portal)(nil)
	_ bridge.TypingPortal              = (*Portal)(nil)
)

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
	portal.latestEventBackfillLock.Lock()
	defer portal.latestEventBackfillLock.Unlock()
	switch {
	case msg.evt != nil:
		portal.handleMessage(msg.source, msg.evt)
	case msg.receipt != nil:
		portal.handleReceipt(msg.receipt, msg.source)
	case msg.undecryptable != nil:
		portal.handleUndecryptableMessage(msg.source, msg.undecryptable)
	case msg.fake != nil:
		msg.fake.ID = "FAKE::" + msg.fake.ID
		portal.handleFakeMessage(*msg.fake)
	default:
		portal.log.Warnln("Unexpected PortalMessage with no message: %+v", msg)
	}
}

func (portal *Portal) handleMatrixMessageLoopItem(msg PortalMatrixMessage) {
	portal.latestEventBackfillLock.Lock()
	defer portal.latestEventBackfillLock.Unlock()
	evtTS := time.UnixMilli(msg.evt.Timestamp)
	timings := messageTimings{
		initReceive:  msg.evt.Mautrix.ReceivedAt.Sub(evtTS),
		decrypt:      msg.evt.Mautrix.DecryptionDuration,
		portalQueue:  time.Since(msg.receivedAt),
		totalReceive: time.Since(evtTS),
	}
	implicitRRStart := time.Now()
	portal.handleMatrixReadReceipt(msg.user, "", evtTS, false)
	timings.implicitRR = time.Since(implicitRRStart)
	switch msg.evt.Type {
	case event.EventMessage, event.EventSticker, TypeMSC3381V2PollResponse, TypeMSC3381PollResponse, TypeMSC3381PollStart:
		portal.HandleMatrixMessage(msg.user, msg.evt, timings)
	case event.EventRedaction:
		portal.HandleMatrixRedaction(msg.user, msg.evt)
	case event.EventReaction:
		portal.HandleMatrixReaction(msg.user, msg.evt)
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
		err := intent.SetReadMarkers(portal.MXID, source.makeReadMarkerContent(msg.MXID, intent.IsCustomPuppet))
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
		waMsg.LiveLocationMessage != nil || waMsg.GroupInviteMessage != nil || waMsg.ContactsArrayMessage != nil ||
		waMsg.HighlyStructuredMessage != nil || waMsg.TemplateMessage != nil || waMsg.TemplateButtonReplyMessage != nil ||
		waMsg.ListMessage != nil || waMsg.ListResponseMessage != nil || waMsg.PollCreationMessage != nil || waMsg.PollCreationMessageV2 != nil
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
	case waMsg.EncReactionMessage != nil:
		return "encrypted reaction"
	case waMsg.PollCreationMessage != nil || waMsg.PollCreationMessageV2 != nil:
		return "poll create"
	case waMsg.PollUpdateMessage != nil:
		return "poll update"
	case waMsg.ProtocolMessage != nil:
		switch waMsg.GetProtocolMessage().GetType() {
		case waProto.ProtocolMessage_REVOKE:
			if waMsg.GetProtocolMessage().GetKey() == nil {
				return "ignore"
			}
			return "revoke"
		case waProto.ProtocolMessage_MESSAGE_EDIT:
			return "edit"
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
	case waMsg.TemplateMessage != nil:
		return portal.convertTemplateMessage(intent, source, info, waMsg.GetTemplateMessage())
	case waMsg.HighlyStructuredMessage != nil:
		return portal.convertTemplateMessage(intent, source, info, waMsg.GetHighlyStructuredMessage().GetHydratedHsm())
	case waMsg.TemplateButtonReplyMessage != nil:
		return portal.convertTemplateButtonReplyMessage(intent, waMsg.GetTemplateButtonReplyMessage())
	case waMsg.ListMessage != nil:
		return portal.convertListMessage(intent, source, waMsg.GetListMessage())
	case waMsg.ListResponseMessage != nil:
		return portal.convertListResponseMessage(intent, waMsg.GetListResponseMessage())
	case waMsg.PollCreationMessage != nil:
		return portal.convertPollCreationMessage(intent, waMsg.GetPollCreationMessage())
	case waMsg.PollCreationMessageV2 != nil:
		return portal.convertPollCreationMessage(intent, waMsg.GetPollCreationMessageV2())
	case waMsg.PollUpdateMessage != nil:
		return portal.convertPollUpdateMessage(intent, source, info, waMsg.GetPollUpdateMessage())
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
	}
	return fmt.Sprintf("Set the disappearing message timer to %s", formatDuration(time.Duration(portal.ExpirationTime)*time.Second))
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
	intent := portal.getMessageIntent(source, &evt.Info, "undecryptable")
	if intent == nil {
		return
	}
	content := undecryptableMessageContent
	resp, err := portal.sendMessage(intent, event.EventMessage, &content, nil, evt.Info.Timestamp.UnixMilli())
	if err != nil {
		portal.log.Errorfln("Failed to send decryption error of %s to Matrix: %v", evt.Info.ID, err)
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
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && msg.Sender.User == portal.Key.Receiver.User && portal.Key.Receiver != portal.Key.JID {
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
	var editTargetMsg *database.Message
	if msgType == "edit" {
		editTargetID := evt.Message.GetProtocolMessage().GetKey().GetId()
		editTargetMsg = portal.bridge.DB.Message.GetByJID(portal.Key, editTargetID)
		if editTargetMsg == nil {
			portal.log.Warnfln("Not handling %s: couldn't find edit target %s", msgID, editTargetID)
			return
		} else if editTargetMsg.Type != database.MsgNormal {
			portal.log.Warnfln("Not handling %s: edit target %s is not a normal message (it's %s)", msgID, editTargetID, editTargetMsg.Type)
			return
		} else if editTargetMsg.Sender.User != evt.Info.Sender.User {
			portal.log.Warnfln("Not handling %s: edit target %s was sent by %s, not %s", msgID, editTargetID, editTargetMsg.Sender.User, evt.Info.Sender.User)
			return
		}
		evt.Message = evt.Message.GetProtocolMessage().GetEditedMessage()
	}

	intent := portal.getMessageIntent(source, &evt.Info, msgType)
	if intent == nil {
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
		if portal.bridge.Config.Bridge.CaptionInMessage {
			converted.MergeCaption()
		}
		var eventID id.EventID
		var lastEventID id.EventID
		if existingMsg != nil {
			portal.MarkDisappearing(nil, existingMsg.MXID, converted.ExpiresIn, evt.Info.Timestamp)
			converted.Content.SetEdit(existingMsg.MXID)
		} else if converted.ReplyTo != nil {
			portal.SetReply(converted.Content, converted.ReplyTo, false)
		}
		dbMsgType := database.MsgNormal
		if editTargetMsg != nil {
			dbMsgType = database.MsgEdit
			converted.Content.SetEdit(editTargetMsg.MXID)
		}
		resp, err := portal.sendMessage(converted.Intent, converted.Type, converted.Content, converted.Extra, evt.Info.Timestamp.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Failed to send %s to Matrix: %v", msgID, err)
		} else {
			if editTargetMsg == nil {
				portal.MarkDisappearing(nil, resp.EventID, converted.ExpiresIn, evt.Info.Timestamp)
			}
			eventID = resp.EventID
			lastEventID = eventID
		}
		// TODO figure out how to handle captions with undecryptable messages turning decryptable
		if converted.Caption != nil && existingMsg == nil && editTargetMsg == nil {
			resp, err = portal.sendMessage(converted.Intent, converted.Type, converted.Caption, nil, evt.Info.Timestamp.UnixMilli())
			if err != nil {
				portal.log.Errorfln("Failed to send caption of %s to Matrix: %v", msgID, err)
			} else {
				portal.MarkDisappearing(nil, resp.EventID, converted.ExpiresIn, evt.Info.Timestamp)
				lastEventID = resp.EventID
			}
		}
		if converted.MultiEvent != nil && existingMsg == nil && editTargetMsg == nil {
			for index, subEvt := range converted.MultiEvent {
				resp, err = portal.sendMessage(converted.Intent, converted.Type, subEvt, nil, evt.Info.Timestamp.UnixMilli())
				if err != nil {
					portal.log.Errorfln("Failed to send sub-event %d of %s to Matrix: %v", index+1, msgID, err)
				} else {
					portal.MarkDisappearing(nil, resp.EventID, converted.ExpiresIn, evt.Info.Timestamp)
					lastEventID = resp.EventID
				}
			}
		}
		if source.MXID == intent.UserID && portal.bridge.Config.Homeserver.Software != bridgeconfig.SoftwareHungry {
			// There are some edge cases (like call notices) where previous messages aren't marked as read
			// when the user sends a message from another device, so just mark the new message as read to be safe.
			// Hungryserv does this automatically, so the bridge doesn't need to do it manually.
			err = intent.SetReadMarkers(portal.MXID, source.makeReadMarkerContent(lastEventID, true))
			if err != nil {
				portal.log.Warnfln("Failed to mark own message %s as read by %s: %v", lastEventID, source.MXID, err)
			}
		}
		if len(eventID) != 0 {
			portal.finishHandling(existingMsg, &evt.Info, eventID, dbMsgType, converted.Error)
		}
	} else if msgType == "reaction" || msgType == "encrypted reaction" {
		if evt.Message.GetEncReactionMessage() != nil {
			decryptedReaction, err := source.Client.DecryptReaction(evt)
			if err != nil {
				portal.log.Errorfln("Failed to decrypt reaction from %s to %s: %v", evt.Info.Sender, evt.Message.GetEncReactionMessage().GetTargetMessageKey().GetId(), err)
			} else {
				portal.HandleMessageReaction(intent, source, &evt.Info, decryptedReaction, existingMsg)
			}
		} else {
			portal.HandleMessageReaction(intent, source, &evt.Info, evt.Message.GetReactionMessage(), existingMsg)
		}
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

func (portal *Portal) markHandled(txn dbutil.Transaction, msg *database.Message, info *types.MessageInfo, mxid id.EventID, isSent, recent bool, msgType database.MessageType, errType database.MessageErrorType) *database.Message {
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

func (portal *Portal) getMessagePuppet(user *User, info *types.MessageInfo) (puppet *Puppet) {
	if info.IsFromMe {
		return portal.bridge.GetPuppetByJID(user.JID)
	} else if portal.IsPrivateChat() {
		puppet = portal.bridge.GetPuppetByJID(portal.Key.JID)
	} else if !info.Sender.IsEmpty() {
		puppet = portal.bridge.GetPuppetByJID(info.Sender)
	}
	if puppet == nil {
		portal.log.Warnfln("Message %+v doesn't seem to have a valid sender (%s): puppet is nil", *info, info.Sender)
		return nil
	}
	user.EnqueuePortalResync(portal)
	puppet.SyncContact(user, true, true, "handling message")
	return puppet
}

func (portal *Portal) getMessageIntent(user *User, info *types.MessageInfo, msgType string) *appservice.IntentAPI {
	puppet := portal.getMessagePuppet(user, info)
	if puppet == nil {
		return nil
	}
	intent := puppet.IntentFor(portal)
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && info.Sender.User == portal.Key.Receiver.User && portal.Key.Receiver != portal.Key.JID {
		portal.log.Debugfln("Not handling %s (%s): user doesn't have double puppeting enabled", info.ID, msgType)
		return nil
	}
	return intent
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

func (portal *Portal) syncParticipant(source *User, participant types.GroupParticipant, puppet *Puppet, user *User, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		if err := recover(); err != nil {
			portal.log.Errorfln("Syncing participant %s panicked: %v\n%s", participant.JID, err, debug.Stack())
		}
	}()
	puppet.SyncContact(source, true, false, "group participant")
	if portal.MXID != "" {
		if user != nil && user != source {
			portal.ensureUserInvited(user)
		}
		if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
			err := puppet.IntentFor(portal).EnsureJoined(portal.MXID)
			if err != nil {
				portal.log.Warnfln("Failed to make puppet of %s join %s: %v", participant.JID, portal.MXID, err)
			}
		}
	}
}

func (portal *Portal) SyncParticipants(source *User, metadata *types.GroupInfo) ([]id.UserID, *event.PowerLevelsEventContent) {
	changed := false
	var levels *event.PowerLevelsEventContent
	var err error
	if portal.MXID != "" {
		levels, err = portal.MainIntent().PowerLevels(portal.MXID)
	}
	if levels == nil || err != nil {
		levels = portal.GetBasePowerLevels()
		changed = true
	}
	changed = portal.applyPowerLevelFixes(levels) || changed
	var wg sync.WaitGroup
	wg.Add(len(metadata.Participants))
	participantMap := make(map[types.JID]bool)
	userIDs := make([]id.UserID, 0, len(metadata.Participants))
	for _, participant := range metadata.Participants {
		portal.log.Debugfln("Syncing participant %s (admin: %t)", participant.JID, participant.IsAdmin)
		participantMap[participant.JID] = true
		puppet := portal.bridge.GetPuppetByJID(participant.JID)
		user := portal.bridge.GetUserByJID(participant.JID)
		if portal.bridge.Config.Bridge.ParallelMemberSync {
			go portal.syncParticipant(source, participant, puppet, user, &wg)
		} else {
			portal.syncParticipant(source, participant, puppet, user, &wg)
		}

		expectedLevel := 0
		if participant.IsSuperAdmin {
			expectedLevel = 95
		} else if participant.IsAdmin {
			expectedLevel = 50
		}
		changed = levels.EnsureUserLevel(puppet.MXID, expectedLevel) || changed
		if user != nil {
			userIDs = append(userIDs, user.MXID)
			changed = levels.EnsureUserLevel(user.MXID, expectedLevel) || changed
		}
		if user == nil || puppet.CustomMXID != user.MXID {
			userIDs = append(userIDs, puppet.MXID)
		}
	}
	if portal.MXID != "" {
		if changed {
			_, err = portal.MainIntent().SetPowerLevels(portal.MXID, levels)
			if err != nil {
				portal.log.Errorln("Failed to change power levels:", err)
			}
		}
		portal.kickExtraUsers(participantMap)
	}
	wg.Wait()
	portal.log.Debugln("Participant sync completed")
	return userIDs, levels
}

func reuploadAvatar(intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
	getResp, err := http.DefaultClient.Get(url)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}
	data, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to read avatar bytes: %w", err)
	}

	resp, err := intent.UploadBytes(data, http.DetectContentType(data))
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}
	return resp.ContentURI, nil
}

func (user *User) updateAvatar(jid types.JID, isCommunity bool, avatarID *string, avatarURL *id.ContentURI, avatarSet *bool, log log.Logger, intent *appservice.IntentAPI) bool {
	currentID := ""
	if *avatarSet && *avatarID != "remove" && *avatarID != "unauthorized" {
		currentID = *avatarID
	}
	avatar, err := user.Client.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{
		Preview:     false,
		ExistingID:  currentID,
		IsCommunity: isCommunity,
	})
	if errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
		if *avatarID == "" {
			*avatarID = "unauthorized"
			*avatarSet = false
			return true
		}
		return false
	} else if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) {
		avatar = &types.ProfilePictureInfo{ID: "remove"}
		if avatar.ID == *avatarID && *avatarSet {
			return false
		}
		*avatarID = avatar.ID
		*avatarURL = id.ContentURI{}
		return true
	} else if err != nil {
		log.Warnln("Failed to get avatar URL:", err)
		return false
	} else if avatar == nil {
		// Avatar hasn't changed
		return false
	}
	if avatar.ID == *avatarID && *avatarSet {
		return false
	} else if len(avatar.URL) == 0 {
		log.Warnln("Didn't get URL in response to avatar query")
		return false
	} else if avatar.ID != *avatarID || avatarURL.IsEmpty() {
		url, err := reuploadAvatar(intent, avatar.URL)
		if err != nil {
			log.Warnln("Failed to reupload avatar:", err)
			return false
		}
		*avatarURL = url
	}
	log.Debugfln("Updated avatar %s -> %s", *avatarID, avatar.ID)
	*avatarID = avatar.ID
	*avatarSet = false
	return true
}

func (portal *Portal) UpdateAvatar(user *User, setBy types.JID, updateInfo bool) bool {
	portal.avatarLock.Lock()
	defer portal.avatarLock.Unlock()
	changed := user.updateAvatar(portal.Key.JID, portal.IsParent, &portal.Avatar, &portal.AvatarURL, &portal.AvatarSet, portal.log, portal.MainIntent())
	if !changed || portal.Avatar == "unauthorized" {
		if changed || updateInfo {
			portal.Update(nil)
		}
		return changed
	}

	if len(portal.MXID) > 0 {
		intent := portal.MainIntent()
		if !setBy.IsEmpty() {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomAvatar(portal.MXID, portal.AvatarURL)
		if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
			_, err = portal.MainIntent().SetRoomAvatar(portal.MXID, portal.AvatarURL)
		}
		if err != nil {
			portal.log.Warnln("Failed to set room avatar:", err)
			return true
		} else {
			portal.AvatarSet = true
		}
	}
	if updateInfo {
		portal.UpdateBridgeInfo()
		portal.Update(nil)
		portal.updateChildRooms()
	}
	return true
}

func (portal *Portal) UpdateName(name string, setBy types.JID, updateInfo bool) bool {
	if name == "" && portal.IsBroadcastList() {
		name = UnnamedBroadcastName
	}
	if portal.Name != name || (!portal.NameSet && len(portal.MXID) > 0) {
		portal.log.Debugfln("Updating name %q -> %q", portal.Name, name)
		portal.Name = name
		portal.NameSet = false
		if updateInfo {
			defer portal.Update(nil)
		}

		if len(portal.MXID) > 0 {
			intent := portal.MainIntent()
			if !setBy.IsEmpty() {
				intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
			}
			_, err := intent.SetRoomName(portal.MXID, name)
			if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
				_, err = portal.MainIntent().SetRoomName(portal.MXID, name)
			}
			if err == nil {
				portal.NameSet = true
				if updateInfo {
					portal.UpdateBridgeInfo()
					portal.updateChildRooms()
				}
				return true
			} else {
				portal.log.Warnln("Failed to set room name:", err)
			}
		}
	}
	return false
}

func (portal *Portal) UpdateTopic(topic string, setBy types.JID, updateInfo bool) bool {
	if portal.Topic != topic || !portal.TopicSet {
		portal.log.Debugfln("Updating topic %q -> %q", portal.Topic, topic)
		portal.Topic = topic
		portal.TopicSet = false

		intent := portal.MainIntent()
		if !setBy.IsEmpty() {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomTopic(portal.MXID, topic)
		if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
			_, err = portal.MainIntent().SetRoomTopic(portal.MXID, topic)
		}
		if err == nil {
			portal.TopicSet = true
			if updateInfo {
				portal.UpdateBridgeInfo()
				portal.Update(nil)
			}
			return true
		} else {
			portal.log.Warnln("Failed to set room topic:", err)
		}
	}
	return false
}

func (portal *Portal) UpdateParentGroup(source *User, parent types.JID, updateInfo bool) bool {
	portal.parentGroupUpdateLock.Lock()
	defer portal.parentGroupUpdateLock.Unlock()
	if portal.ParentGroup != parent {
		portal.log.Debugfln("Updating parent group %v -> %v", portal.ParentGroup, parent)
		portal.updateCommunitySpace(source, false, false)
		portal.ParentGroup = parent
		portal.parentPortal = nil
		portal.InSpace = false
		portal.updateCommunitySpace(source, true, false)
		if updateInfo {
			portal.UpdateBridgeInfo()
			portal.Update(nil)
		}
		return true
	} else if !portal.ParentGroup.IsEmpty() && !portal.InSpace {
		return portal.updateCommunitySpace(source, true, updateInfo)
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
	update = portal.UpdateParentGroup(user, groupInfo.LinkedParentJID, false) || update
	if portal.ExpirationTime != groupInfo.DisappearingTimer {
		update = true
		portal.ExpirationTime = groupInfo.DisappearingTimer
	}
	if portal.IsParent != groupInfo.IsParent {
		if portal.MXID != "" {
			portal.log.Warnfln("Existing group changed is_parent from %t to %t", portal.IsParent, groupInfo.IsParent)
		}
		portal.IsParent = groupInfo.IsParent
		update = true
	}

	portal.RestrictMessageSending(groupInfo.IsAnnounce)
	portal.RestrictMetadataChanges(groupInfo.IsLocked)

	return update
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
	go portal.addToPersonalSpace(user)

	update := false
	update = portal.UpdateMetadata(user, groupInfo) || update
	if !portal.IsPrivateChat() && !portal.IsBroadcastList() {
		update = portal.UpdateAvatar(user, types.EmptyJID, false) || update
	}
	if update || portal.LastSync.Add(24*time.Hour).Before(time.Now()) {
		portal.LastSync = time.Now()
		portal.Update(nil)
		portal.UpdateBridgeInfo()
		portal.updateChildRooms()
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
			event.StateRoomName.Type:     anyone,
			event.StateRoomAvatar.Type:   anyone,
			event.StateTopic.Type:        anyone,
			event.EventReaction.Type:     anyone,
			event.EventRedaction.Type:    anyone,
			TypeMSC3381PollResponse.Type: anyone,
		},
	}
}

func (portal *Portal) applyPowerLevelFixes(levels *event.PowerLevelsEventContent) bool {
	changed := false
	changed = levels.EnsureEventLevel(event.EventReaction, 0) || changed
	changed = levels.EnsureEventLevel(event.EventRedaction, 0) || changed
	changed = levels.EnsureEventLevel(TypeMSC3381PollResponse, 0) || changed
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

func (portal *Portal) getBridgeInfoStateKey() string {
	return fmt.Sprintf("net.maunium.whatsapp://whatsapp/%s", portal.Key.JID)
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
	if parent := portal.GetParentPortal(); parent != nil {
		bridgeInfo.Network = &event.BridgeInfoSection{
			ID:          parent.Key.JID.String(),
			DisplayName: parent.Name,
			AvatarURL:   parent.AvatarURL.CUString(),
		}
	}
	return portal.getBridgeInfoStateKey(), bridgeInfo
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

func (portal *Portal) updateChildRooms() {
	if !portal.IsParent {
		return
	}
	children := portal.bridge.GetAllByParentGroup(portal.Key.JID)
	for _, child := range children {
		changed := child.updateCommunitySpace(nil, true, false)
		child.UpdateBridgeInfo()
		if changed {
			portal.Update(nil)
		}
	}
}

func (portal *Portal) GetEncryptionEventContent() (evt *event.EncryptionEventContent) {
	evt = &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}
	if rot := portal.bridge.Config.Bridge.Encryption.Rotation; rot.EnableCustom {
		evt.RotationPeriodMillis = rot.Milliseconds
		evt.RotationPeriodMessages = rot.Messages
	}
	return
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
		if portal.bridge.Config.Bridge.PrivateChatPortalMeta || portal.bridge.Config.Bridge.Encryption.Default {
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
			portal.IsParent = groupInfo.IsParent
			portal.ParentGroup = groupInfo.LinkedParentJID
			if groupInfo.IsEphemeral {
				portal.ExpirationTime = groupInfo.DisappearingTimer
			}
		}
		portal.UpdateAvatar(user, types.EmptyJID, false)
	}

	powerLevels := portal.GetBasePowerLevels()

	if groupInfo != nil {
		if groupInfo.IsAnnounce {
			powerLevels.EventsDefault = 50
		}
		if groupInfo.IsLocked {
			powerLevels.EnsureEventLevel(event.StateRoomName, 50)
			powerLevels.EnsureEventLevel(event.StateRoomAvatar, 50)
			powerLevels.EnsureEventLevel(event.StateTopic, 50)
		}
	}

	bridgeInfoStateKey, bridgeInfo := portal.getBridgeInfo()

	initialState := []*event.Event{{
		Type: event.StatePowerLevels,
		Content: event.Content{
			Parsed: powerLevels,
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
		portal.AvatarSet = true
	}

	var invite []id.UserID

	if portal.bridge.Config.Bridge.Encryption.Default {
		initialState = append(initialState, &event.Event{
			Type: event.StateEncryption,
			Content: event.Content{
				Parsed: portal.GetEncryptionEventContent(),
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
	if portal.IsParent {
		creationContent["type"] = event.RoomTypeSpace
	} else if parent := portal.GetParentPortal(); parent != nil && parent.MXID != "" {
		initialState = append(initialState, &event.Event{
			Type:     event.StateSpaceParent,
			StateKey: proto.String(parent.MXID.String()),
			Content: event.Content{
				Parsed: &event.SpaceParentEventContent{
					Via:       []string{portal.bridge.Config.Homeserver.Domain},
					Canonical: true,
				},
			},
		})
	}
	autoJoinInvites := portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry
	if autoJoinInvites {
		portal.log.Debugfln("Hungryserv mode: adding all group members in create request")
		if groupInfo != nil {
			// TODO non-hungryserv could also include all members in invites, and then send joins manually?
			participants, powerLevels := portal.SyncParticipants(user, groupInfo)
			invite = append(invite, participants...)
			if initialState[0].Type != event.StatePowerLevels {
				panic(fmt.Errorf("unexpected type %s in first initial state event", initialState[0].Type.Type))
			}
			initialState[0].Content.Parsed = powerLevels
		} else {
			invite = append(invite, user.MXID)
		}
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

		BeeperAutoJoinInvites: autoJoinInvites,
	})
	if err != nil {
		return err
	}
	portal.log.Infoln("Matrix room created:", resp.RoomID)
	portal.InSpace = false
	portal.NameSet = len(portal.Name) > 0
	portal.TopicSet = len(portal.Topic) > 0
	portal.MXID = resp.RoomID
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()
	portal.Update(nil)

	// We set the memberships beforehand to make sure the encryption key exchange in initial backfill knows the users are here.
	inviteMembership := event.MembershipInvite
	if autoJoinInvites {
		inviteMembership = event.MembershipJoin
	}
	for _, userID := range invite {
		portal.bridge.StateStore.SetMembership(portal.MXID, userID, inviteMembership)
	}

	if !autoJoinInvites {
		portal.ensureUserInvited(user)
	}
	user.syncChatDoublePuppetDetails(portal, true)

	go portal.updateCommunitySpace(user, true, true)
	go portal.addToPersonalSpace(user)

	if groupInfo != nil && !autoJoinInvites {
		portal.SyncParticipants(user, groupInfo)
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
	} else if portal.IsParent {
		portal.updateChildRooms()
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
		user.EnqueueImmediateBackfills(portals)
		user.EnqueueDeferredBackfills(portals)
		user.BackfillQueue.ReCheck()
	}
	return nil
}

func (portal *Portal) addToPersonalSpace(user *User) {
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

func (portal *Portal) removeSpaceParentEvent(space id.RoomID) {
	_, err := portal.MainIntent().SendStateEvent(portal.MXID, event.StateSpaceParent, space.String(), &event.SpaceParentEventContent{})
	if err != nil {
		portal.log.Warnfln("Failed to send m.space.parent event to remove portal from %s: %v", space, err)
	}
}

func (portal *Portal) updateCommunitySpace(user *User, add, updateInfo bool) bool {
	if add == portal.InSpace {
		return false
	}
	space := portal.GetParentPortal()
	if space == nil {
		return false
	} else if space.MXID == "" {
		if !add || user == nil {
			return false
		}
		portal.log.Debugfln("Creating portal for parent group %v", space.Key.JID)
		err := space.CreateMatrixRoom(user, nil, false, false)
		if err != nil {
			portal.log.Debugfln("Failed to create portal for parent group: %v", err)
			return false
		}
	}

	var action string
	var parentContent event.SpaceParentEventContent
	var childContent event.SpaceChildEventContent
	if add {
		parentContent.Canonical = true
		parentContent.Via = []string{portal.bridge.Config.Homeserver.Domain}
		childContent.Via = []string{portal.bridge.Config.Homeserver.Domain}
		action = "add portal to"
		portal.log.Debugfln("Adding %s to space %s (%s)", portal.MXID, space.MXID, space.Key.JID)
	} else {
		action = "remove portal from"
		portal.log.Debugfln("Removing %s from space %s (%s)", portal.MXID, space.MXID, space.Key.JID)
	}

	_, err := space.MainIntent().SendStateEvent(space.MXID, event.StateSpaceChild, portal.MXID.String(), &childContent)
	if err != nil {
		portal.log.Errorfln("Failed to send m.space.child event to %s %s: %v", action, space.MXID, err)
		return false
	}
	_, err = portal.MainIntent().SendStateEvent(portal.MXID, event.StateSpaceParent, space.MXID.String(), &parentContent)
	if err != nil {
		portal.log.Warnfln("Failed to send m.space.parent event to %s %s: %v", action, space.MXID, err)
	}
	portal.InSpace = add
	if updateInfo {
		portal.Update(nil)
		portal.UpdateBridgeInfo()
	}
	return true
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

func (portal *Portal) GetParentPortal() *Portal {
	if portal.ParentGroup.IsEmpty() {
		return nil
	} else if portal.parentPortal == nil {
		portal.parentPortal = portal.bridge.GetPortalByJID(database.NewPortalKey(portal.ParentGroup, portal.ParentGroup))
	}
	return portal.parentPortal
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.bridge.GetPuppetByJID(portal.Key.JID).DefaultIntent()
	}
	return portal.bridge.Bot
}

func (portal *Portal) SetReply(content *event.MessageEventContent, replyTo *ReplyInfo, isBackfill bool) bool {
	if replyTo == nil {
		return false
	}
	key := portal.Key
	targetPortal := portal
	defer func() {
		if content.RelatesTo != nil && content.RelatesTo.InReplyTo != nil && targetPortal != portal {
			content.RelatesTo.InReplyTo.UnstableRoomID = targetPortal.MXID
		}
	}()
	if portal.bridge.Config.Bridge.CrossRoomReplies && !replyTo.Chat.IsEmpty() && replyTo.Chat != key.JID {
		if replyTo.Chat.Server == types.GroupServer {
			key = database.NewPortalKey(replyTo.Chat, types.EmptyJID)
		} else if replyTo.Chat == types.BroadcastServerJID {
			key = database.NewPortalKey(replyTo.Chat, key.Receiver)
		}
		if key != portal.Key {
			targetPortal = portal.bridge.GetExistingPortalByJID(key)
			if targetPortal == nil {
				return false
			}
		}
	}
	message := portal.bridge.DB.Message.GetByJID(key, replyTo.MessageID)
	if message == nil || message.IsFakeMXID() {
		if isBackfill && portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
			content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(targetPortal.deterministicEventID(replyTo.Sender, replyTo.MessageID, ""))
			return true
		}
		return false
	}
	evt, err := targetPortal.MainIntent().GetEvent(targetPortal.MXID, message.MXID)
	if err != nil {
		portal.log.Warnln("Failed to get reply target:", err)
		content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(message.MXID)
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

		resp, err := intent.RedactEvent(portal.MXID, existing.MXID)
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

		var content event.ReactionEventContent
		content.RelatesTo = event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: target.MXID,
			Key:     variationselector.Add(reaction.GetText()),
		}
		resp, err := intent.SendMassagedMessageEvent(portal.MXID, event.EventReaction, &content, info.Timestamp.UnixMilli())
		if err != nil {
			portal.log.Errorfln("Failed to bridge reaction %s from %s to %s: %v", info.ID, info.Sender, target.JID, err)
			return
		}

		portal.finishHandling(existingMsg, info, resp.EventID, database.MsgReaction, database.MsgNoError)
		portal.upsertReaction(nil, intent, target.JID, info.Sender, resp.EventID, info.ID)
	}
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

func (portal *Portal) deleteForMe(user *User, content *events.DeleteForMe) bool {
	matrixUsers, err := portal.GetMatrixUsers()
	if err != nil {
		portal.log.Errorln("Failed to get Matrix users in portal to see if DeleteForMe should be handled:", err)
		return false
	}
	if len(matrixUsers) == 1 && matrixUsers[0] == user.MXID {
		msg := portal.bridge.DB.Message.GetByJID(portal.Key, content.MessageID)
		if msg == nil || msg.IsFakeMXID() {
			return false
		}
		_, err := portal.MainIntent().RedactEvent(portal.MXID, msg.MXID)
		if err != nil {
			portal.log.Errorln("Failed to redact %s: %v", msg.JID, err)
		} else {
			msg.Delete()
		}
		return true
	}
	return false
}

func (portal *Portal) sendMainIntentMessage(content *event.MessageEventContent) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(portal.MainIntent(), event.EventMessage, content, nil, 0)
}

func (portal *Portal) encrypt(intent *appservice.IntentAPI, content *event.Content, eventType event.Type) (event.Type, error) {
	if !portal.Encrypted || portal.bridge.Crypto == nil {
		return eventType, nil
	}
	intent.AddDoublePuppetValue(content)
	// TODO maybe the locking should be inside mautrix-go?
	portal.encryptLock.Lock()
	defer portal.encryptLock.Unlock()
	err := portal.bridge.Crypto.Encrypt(portal.MXID, eventType, content)
	if err != nil {
		return eventType, fmt.Errorf("failed to encrypt event: %w", err)
	}
	return event.EventEncrypted, nil
}

func (portal *Portal) sendMessage(intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	var err error
	eventType, err = portal.encrypt(intent, &wrappedContent, eventType)
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

type ReplyInfo struct {
	MessageID types.MessageID
	Chat      types.JID
	Sender    types.JID
}

type Replyable interface {
	GetStanzaId() string
	GetParticipant() string
	GetRemoteJid() string
}

func GetReply(replyable Replyable) *ReplyInfo {
	if replyable.GetStanzaId() == "" {
		return nil
	}
	sender, err := types.ParseJID(replyable.GetParticipant())
	if err != nil {
		return nil
	}
	chat, _ := types.ParseJID(replyable.GetRemoteJid())
	return &ReplyInfo{
		MessageID: types.MessageID(replyable.GetStanzaId()),
		Chat:      chat,
		Sender:    sender,
	}
}

type ConvertedMessage struct {
	Intent  *appservice.IntentAPI
	Type    event.Type
	Content *event.MessageEventContent
	Extra   map[string]interface{}
	Caption *event.MessageEventContent

	MultiEvent []*event.MessageEventContent

	ReplyTo   *ReplyInfo
	ExpiresIn time.Duration
	Error     database.MessageErrorType
	MediaKey  []byte
}

func (cm *ConvertedMessage) MergeCaption() {
	if cm.Caption == nil {
		return
	}
	cm.Content.FileName = cm.Content.Body
	extensibleCaption := map[string]interface{}{
		"org.matrix.msc1767.text": cm.Caption.Body,
	}
	cm.Extra["org.matrix.msc1767.caption"] = extensibleCaption
	cm.Content.Body = cm.Caption.Body
	if cm.Caption.Format == event.FormatHTML {
		cm.Content.Format = event.FormatHTML
		cm.Content.FormattedBody = cm.Caption.FormattedBody
		extensibleCaption["org.matrix.msc1767.html"] = cm.Caption.FormattedBody
	}
	cm.Caption = nil
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
	portal.bridge.Formatter.ParseWhatsApp(portal.MXID, content, contextInfo.GetMentionedJid(), false, false)
	expiresIn := time.Duration(contextInfo.GetExpiration()) * time.Second
	extraAttrs := map[string]interface{}{}
	extraAttrs["com.beeper.linkpreviews"] = portal.convertURLPreviewToBeeper(intent, source, msg.GetExtendedTextMessage())

	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		ReplyTo:   GetReply(contextInfo),
		ExpiresIn: expiresIn,
		Extra:     extraAttrs,
	}
}

func (portal *Portal) convertTemplateMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, tplMsg *waProto.TemplateMessage) *ConvertedMessage {
	converted := &ConvertedMessage{
		Intent: intent,
		Type:   event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    "Unsupported business message",
			MsgType: event.MsgText,
		},
		ReplyTo:   GetReply(tplMsg.GetContextInfo()),
		ExpiresIn: time.Duration(tplMsg.GetContextInfo().GetExpiration()) * time.Second,
	}

	tpl := tplMsg.GetHydratedTemplate()
	if tpl == nil {
		return converted
	}
	content := tpl.GetHydratedContentText()
	if buttons := tpl.GetHydratedButtons(); len(buttons) > 0 {
		addButtonText := false
		descriptions := make([]string, len(buttons))
		for i, rawButton := range buttons {
			switch button := rawButton.GetHydratedButton().(type) {
			case *waProto.HydratedTemplateButton_QuickReplyButton:
				descriptions[i] = fmt.Sprintf("<%s>", button.QuickReplyButton.GetDisplayText())
				addButtonText = true
			case *waProto.HydratedTemplateButton_UrlButton:
				descriptions[i] = fmt.Sprintf("[%s](%s)", button.UrlButton.GetDisplayText(), button.UrlButton.GetUrl())
			case *waProto.HydratedTemplateButton_CallButton:
				descriptions[i] = fmt.Sprintf("[%s](tel:%s)", button.CallButton.GetDisplayText(), button.CallButton.GetPhoneNumber())
			}
		}
		description := strings.Join(descriptions, " - ")
		if addButtonText {
			description += "\nUse the WhatsApp app to click buttons"
		}
		content = fmt.Sprintf("%s\n\n%s", content, description)
	}
	if footer := tpl.GetHydratedFooterText(); footer != "" {
		content = fmt.Sprintf("%s\n\n%s", content, footer)
	}

	var convertedTitle *ConvertedMessage
	switch title := tpl.GetTitle().(type) {
	case *waProto.TemplateMessage_HydratedFourRowTemplate_DocumentMessage:
		convertedTitle = portal.convertMediaMessage(intent, source, info, title.DocumentMessage, "file attachment", false)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_ImageMessage:
		convertedTitle = portal.convertMediaMessage(intent, source, info, title.ImageMessage, "photo", false)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_VideoMessage:
		convertedTitle = portal.convertMediaMessage(intent, source, info, title.VideoMessage, "video attachment", false)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_LocationMessage:
		content = fmt.Sprintf("Unsupported location message\n\n%s", content)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText:
		content = fmt.Sprintf("%s\n\n%s", title.HydratedTitleText, content)
	}

	converted.Content.Body = content
	portal.bridge.Formatter.ParseWhatsApp(portal.MXID, converted.Content, nil, true, false)
	if convertedTitle != nil {
		converted.MediaKey = convertedTitle.MediaKey
		converted.Extra = convertedTitle.Extra
		converted.Caption = converted.Content
		converted.Content = convertedTitle.Content
		converted.Error = convertedTitle.Error
	}
	if converted.Extra == nil {
		converted.Extra = make(map[string]interface{})
	}
	converted.Extra["fi.mau.whatsapp.hydrated_template_id"] = tpl.GetTemplateId()
	return converted
}

func (portal *Portal) convertTemplateButtonReplyMessage(intent *appservice.IntentAPI, msg *waProto.TemplateButtonReplyMessage) *ConvertedMessage {
	return &ConvertedMessage{
		Intent: intent,
		Type:   event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    msg.GetSelectedDisplayText(),
			MsgType: event.MsgText,
		},
		Extra: map[string]interface{}{
			"fi.mau.whatsapp.template_button_reply": map[string]interface{}{
				"id":    msg.GetSelectedId(),
				"index": msg.GetSelectedIndex(),
			},
		},
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
	}
}

func (portal *Portal) convertListMessage(intent *appservice.IntentAPI, source *User, msg *waProto.ListMessage) *ConvertedMessage {
	converted := &ConvertedMessage{
		Intent: intent,
		Type:   event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    "Unsupported business message",
			MsgType: event.MsgText,
		},
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
	}
	body := msg.GetDescription()
	if msg.GetTitle() != "" {
		if body == "" {
			body = msg.GetTitle()
		} else {
			body = fmt.Sprintf("%s\n\n%s", msg.GetTitle(), body)
		}
	}
	randomID := util.RandomString(64)
	body = fmt.Sprintf("%s\n%s", body, randomID)
	if msg.GetFooterText() != "" {
		body = fmt.Sprintf("%s\n\n%s", body, msg.GetFooterText())
	}
	converted.Content.Body = body
	portal.bridge.Formatter.ParseWhatsApp(portal.MXID, converted.Content, nil, false, true)

	var optionsMarkdown strings.Builder
	_, _ = fmt.Fprintf(&optionsMarkdown, "#### %s\n", msg.GetButtonText())
	for _, section := range msg.GetSections() {
		nesting := ""
		if section.GetTitle() != "" {
			_, _ = fmt.Fprintf(&optionsMarkdown, "* %s\n", section.GetTitle())
			nesting = "  "
		}
		for _, row := range section.GetRows() {
			if row.GetDescription() != "" {
				_, _ = fmt.Fprintf(&optionsMarkdown, "%s* %s: %s\n", nesting, row.GetTitle(), row.GetDescription())
			} else {
				_, _ = fmt.Fprintf(&optionsMarkdown, "%s* %s\n", nesting, row.GetTitle())
			}
		}
	}
	optionsMarkdown.WriteString("\nUse the WhatsApp app to respond")
	rendered := format.RenderMarkdown(optionsMarkdown.String(), true, false)
	converted.Content.Body = strings.Replace(converted.Content.Body, randomID, rendered.Body, 1)
	converted.Content.FormattedBody = strings.Replace(converted.Content.FormattedBody, randomID, rendered.FormattedBody, 1)
	return converted
}

func (portal *Portal) convertListResponseMessage(intent *appservice.IntentAPI, msg *waProto.ListResponseMessage) *ConvertedMessage {
	var body string
	if msg.GetTitle() != "" {
		if msg.GetDescription() != "" {
			body = fmt.Sprintf("%s\n\n%s", msg.GetTitle(), msg.GetDescription())
		} else {
			body = msg.GetTitle()
		}
	} else if msg.GetDescription() != "" {
		body = msg.GetDescription()
	} else {
		body = "Unsupported list reply message"
	}
	return &ConvertedMessage{
		Intent: intent,
		Type:   event.EventMessage,
		Content: &event.MessageEventContent{
			Body:    body,
			MsgType: event.MsgText,
		},
		Extra: map[string]interface{}{
			"fi.mau.whatsapp.list_reply": map[string]interface{}{
				"row_id": msg.GetSingleSelectReply().GetSelectedRowId(),
			},
		},
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
	}
}

func (portal *Portal) convertPollUpdateMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, msg *waProto.PollUpdateMessage) *ConvertedMessage {
	pollMessage := portal.bridge.DB.Message.GetByJID(portal.Key, msg.GetPollCreationMessageKey().GetId())
	if pollMessage == nil {
		portal.log.Warnfln("Failed to convert vote message %s: poll message %s not found", info.ID, msg.GetPollCreationMessageKey().GetId())
		return nil
	}
	vote, err := source.Client.DecryptPollVote(&events.Message{
		Info:    *info,
		Message: &waProto.Message{PollUpdateMessage: msg},
	})
	if err != nil {
		portal.log.Errorfln("Failed to decrypt vote message %s: %v", info.ID, err)
		return nil
	}
	selectedHashes := make([]string, len(vote.GetSelectedOptions()))
	if pollMessage.Type == database.MsgMatrixPoll {
		mappedAnswers := pollMessage.GetPollOptionIDs(vote.GetSelectedOptions())
		for i, opt := range vote.GetSelectedOptions() {
			if len(opt) != 32 {
				portal.log.Warnfln("Unexpected option hash length %d in %s's vote to %s", len(opt), info.Sender, pollMessage.MXID)
				continue
			}
			var ok bool
			selectedHashes[i], ok = mappedAnswers[*(*[32]byte)(opt)]
			if !ok {
				portal.log.Warnfln("Didn't find ID for option %X in %s's vote to %s", opt, info.Sender, pollMessage.MXID)
			}
		}
	} else {
		for i, opt := range vote.GetSelectedOptions() {
			selectedHashes[i] = hex.EncodeToString(opt)
		}
	}

	evtType := TypeMSC3381PollResponse
	//if portal.bridge.Config.Bridge.ExtEvPolls == 2 {
	//	evtType = TypeMSC3381V2PollResponse
	//}
	return &ConvertedMessage{
		Intent: intent,
		Type:   evtType,
		Content: &event.MessageEventContent{
			RelatesTo: &event.RelatesTo{
				Type:    event.RelReference,
				EventID: pollMessage.MXID,
			},
		},
		Extra: map[string]any{
			"org.matrix.msc3381.poll.response": map[string]any{
				"answers": selectedHashes,
			},
			"org.matrix.msc3381.v2.selections": selectedHashes,
		},
	}
}

func (portal *Portal) convertPollCreationMessage(intent *appservice.IntentAPI, msg *waProto.PollCreationMessage) *ConvertedMessage {
	optionNames := make([]string, len(msg.GetOptions()))
	optionsListText := make([]string, len(optionNames))
	optionsListHTML := make([]string, len(optionNames))
	msc3381Answers := make([]map[string]any, len(optionNames))
	msc3381V2Answers := make([]map[string]any, len(optionNames))
	for i, opt := range msg.GetOptions() {
		optionNames[i] = opt.GetOptionName()
		optionsListText[i] = fmt.Sprintf("%d. %s\n", i+1, optionNames[i])
		optionsListHTML[i] = fmt.Sprintf("<li>%s</li>", event.TextToHTML(optionNames[i]))
		optionHash := sha256.Sum256([]byte(opt.GetOptionName()))
		optionHashStr := hex.EncodeToString(optionHash[:])
		msc3381Answers[i] = map[string]any{
			"id":                      optionHashStr,
			"org.matrix.msc1767.text": opt.GetOptionName(),
		}
		msc3381V2Answers[i] = map[string]any{
			"org.matrix.msc3381.v2.id": optionHashStr,
			"org.matrix.msc1767.markup": []map[string]any{
				{"mimetype": "text/plain", "body": opt.GetOptionName()},
			},
		}
	}
	body := fmt.Sprintf("%s\n\n%s", msg.GetName(), strings.Join(optionsListText, "\n"))
	formattedBody := fmt.Sprintf("<p>%s</p><ol>%s</ol>", event.TextToHTML(msg.GetName()), strings.Join(optionsListHTML, ""))
	maxChoices := int(msg.GetSelectableOptionsCount())
	if maxChoices <= 0 {
		maxChoices = len(optionNames)
	}
	evtType := event.EventMessage
	if portal.bridge.Config.Bridge.ExtEvPolls {
		evtType = TypeMSC3381PollStart
	}
	//else if portal.bridge.Config.Bridge.ExtEvPolls == 2 {
	//	evtType.Type = "org.matrix.msc3381.v2.poll.start"
	//}
	return &ConvertedMessage{
		Intent: intent,
		Type:   evtType,
		Content: &event.MessageEventContent{
			Body:          body,
			MsgType:       event.MsgText,
			Format:        event.FormatHTML,
			FormattedBody: formattedBody,
		},
		Extra: map[string]any{
			// Custom metadata
			"fi.mau.whatsapp.poll": map[string]any{
				"option_names":             optionNames,
				"selectable_options_count": msg.GetSelectableOptionsCount(),
			},

			// Current extensible events (as of November 2022)
			"org.matrix.msc1767.markup": []map[string]any{
				{"mimetype": "text/html", "body": formattedBody},
				{"mimetype": "text/plain", "body": body},
			},
			"org.matrix.msc3381.v2.poll": map[string]any{
				"kind":           "org.matrix.msc3381.v2.disclosed",
				"max_selections": maxChoices,
				"question": map[string]any{
					"org.matrix.msc1767.markup": []map[string]any{
						{"mimetype": "text/plain", "body": msg.GetName()},
					},
				},
				"answers": msc3381V2Answers,
			},

			// Legacy extensible events
			"org.matrix.msc1767.message": []map[string]any{
				{"mimetype": "text/html", "body": formattedBody},
				{"mimetype": "text/plain", "body": body},
			},
			"org.matrix.msc3381.poll.start": map[string]any{
				"kind":           "org.matrix.msc3381.poll.disclosed",
				"max_selections": maxChoices,
				"question": map[string]any{
					"org.matrix.msc1767.text": msg.GetName(),
				},
				"answers": msc3381Answers,
			},
		},
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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
	htmlMessage := fmt.Sprintf(inviteMsg, event.TextToHTML(msg.GetCaption()), msg.GetGroupName(), expiry)
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
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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
		ReplyTo:    GetReply(msg.GetContextInfo()),
		ExpiresIn:  time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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
				_, _ = targetIntent.LeaveRoom(portal.MXID)
			}
		}
	} else {
		_, err := targetIntent.LeaveRoom(portal.MXID)
		if err != nil {
			portal.log.Warnfln("Failed to leave portal as %s: %v", target, err)
			_, _ = portal.MainIntent().KickUser(portal.MXID, &mautrix.ReqKickUser{UserID: target})
		}
	}
	portal.CleanupIfEmpty()
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

func (portal *Portal) HandleWhatsAppInvite(source *User, senderJID *types.JID, jids []types.JID) (evtID id.EventID) {
	intent := portal.MainIntent()
	if senderJID != nil && !senderJID.IsEmpty() {
		sender := portal.bridge.GetPuppetByJID(*senderJID)
		intent = sender.IntentFor(portal)
	}
	for _, jid := range jids {
		puppet := portal.bridge.GetPuppetByJID(jid)
		puppet.SyncContact(source, true, false, "handling whatsapp invite")
		resp, err := intent.SendStateEvent(portal.MXID, event.StateMember, puppet.MXID.String(), &event.MemberEventContent{
			Membership:  event.MembershipInvite,
			Displayname: puppet.Displayname,
			AvatarURL:   puppet.AvatarURL.CUString(),
		})
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

func (portal *Portal) HandleWhatsAppDeleteChat(user *User) {
	if portal.MXID == "" {
		return
	}
	matrixUsers, err := portal.GetMatrixUsers()
	if err != nil {
		portal.log.Errorln("Failed to get Matrix users to see if DeleteChat should be handled:", err)
		return
	}
	if len(matrixUsers) > 1 {
		portal.log.Infoln("Portal contains more than one Matrix user, so deleteChat will not be handled.")
		return
	} else if (len(matrixUsers) == 1 && matrixUsers[0] == user.MXID) || len(matrixUsers) < 1 {
		portal.log.Debugln("User deleted chat and there are no other Matrix users using it, deleting portal...")
		portal.Delete()
		portal.Cleanup(false)
	}
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
	if errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith403) || errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith410) {
		portal.log.Debugfln("Failed to bridge media for %s: %v", info.ID, bridgeErr)
	} else {
		portal.log.Errorfln("Failed to bridge media for %s: %v", info.ID, bridgeErr)
	}
	if keys != nil {
		if portal.bridge.Config.Bridge.CaptionInMessage {
			converted.MergeCaption()
		}
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

const WhatsAppStickerSize = 190

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
			"fi.mau.gif":           true,
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

	eventType := event.EventMessage
	switch msg.(type) {
	case *waProto.ImageMessage:
		content.MsgType = event.MsgImage
	case *waProto.StickerMessage:
		eventType = event.EventSticker
		if content.Info.Width > content.Info.Height {
			content.Info.Height /= content.Info.Width / WhatsAppStickerSize
			content.Info.Width = WhatsAppStickerSize
		} else if content.Info.Width < content.Info.Height {
			content.Info.Width /= content.Info.Height / WhatsAppStickerSize
			content.Info.Height = WhatsAppStickerSize
		} else {
			content.Info.Width = WhatsAppStickerSize
			content.Info.Height = WhatsAppStickerSize
		}
	case *waProto.VideoMessage:
		content.MsgType = event.MsgVideo
	case *waProto.AudioMessage:
		content.MsgType = event.MsgAudio
	case *waProto.DocumentMessage:
		content.MsgType = event.MsgFile
	default:
		portal.log.Warnfln("Unexpected media type %T in convertMediaMessageContent", msg)
		content.MsgType = event.MsgFile
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

		portal.bridge.Formatter.ParseWhatsApp(portal.MXID, captionContent, msg.GetContextInfo().GetMentionedJid(), false, false)
	}

	return &ConvertedMessage{
		Intent:    intent,
		Type:      eventType,
		Content:   content,
		Caption:   captionContent,
		ReplyTo:   GetReply(msg.GetContextInfo()),
		ExpiresIn: time.Duration(msg.GetContextInfo().GetExpiration()) * time.Second,
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

	// This is a hack for bad clients like Element iOS that require a thumbnail (https://github.com/vector-im/element-ios/issues/4004)
	if strings.HasPrefix(content.Info.MimeType, "image/") && content.Info.ThumbnailInfo == nil {
		infoCopy := *content.Info
		content.Info.ThumbnailInfo = &infoCopy
		if content.File != nil {
			content.Info.ThumbnailFile = file
		} else {
			content.Info.ThumbnailURL = content.URL
		}
	}
	return nil
}

func (portal *Portal) convertMediaMessage(intent *appservice.IntentAPI, source *User, info *types.MessageInfo, msg MediaMessage, typeName string, isBackfill bool) *ConvertedMessage {
	converted := portal.convertMediaMessageContent(intent, msg)
	if msg.GetFileLength() > uint64(portal.bridge.MediaConfig.UploadSize) {
		return portal.makeMediaBridgeFailureMessage(info, errors.New("file is too large"), converted, nil, fmt.Sprintf("Large %s not bridged - please use WhatsApp app to view", typeName))
	}
	data, err := source.Client.Download(msg)
	if errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410) {
		converted.Error = database.MsgErrMediaNotFound
		converted.MediaKey = msg.GetMediaKey()

		errorText := fmt.Sprintf("Old %s.", typeName)
		if portal.bridge.Config.Bridge.HistorySync.MediaRequests.AutoRequestMedia && isBackfill {
			errorText += " Media will be automatically requested from your phone later."
		} else {
			errorText += " React with the \u267b (recycle) emoji to request this media from your phone."
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
		errorName := waProto.MediaRetryNotification_ResultType_name[int32(retryData.GetResult())]
		if retryData.GetDirectPath() == "" {
			portal.log.Warnfln("Got error response in media retry notification for %s: %s", retry.MessageID, errorName)
			portal.log.Debugfln("Error response contents: %+v", retryData)
			if retryData.GetResult() == waProto.MediaRetryNotification_NOT_FOUND {
				portal.sendMediaRetryFailureEdit(intent, msg, whatsmeow.ErrMediaNotAvailableOnPhone)
			} else {
				portal.sendMediaRetryFailureEdit(intent, msg, fmt.Errorf("phone sent error response: %s", errorName))
			}
			return
		} else {
			portal.log.Debugfln("Got error response %s in media retry notification for %s, but response also contains a new download URL - trying to download", retry.MessageID, errorName)
		}
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

func createThumbnailAndGetSize(source []byte, pngThumbnail bool) ([]byte, int, int, error) {
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
	if pngThumbnail {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpeg.DefaultQuality})
	}
	if err != nil {
		return nil, width, height, fmt.Errorf("failed to re-encode thumbnail: %w", err)
	}
	return buf.Bytes(), width, height, nil
}

func createThumbnail(source []byte, png bool) ([]byte, error) {
	data, _, _, err := createThumbnailAndGetSize(source, png)
	return data, err
}

func (portal *Portal) downloadThumbnail(ctx context.Context, original []byte, thumbnailURL id.ContentURIString, eventID id.EventID, png bool) ([]byte, error) {
	if len(thumbnailURL) == 0 {
		// just fall back to making thumbnail of original
	} else if mxc, err := thumbnailURL.Parse(); err != nil {
		portal.log.Warnfln("Malformed thumbnail URL in %s: %v (falling back to generating thumbnail from source)", eventID, err)
	} else if thumbnail, err := portal.MainIntent().DownloadBytesContext(ctx, mxc); err != nil {
		portal.log.Warnfln("Failed to download thumbnail in %s: %v (falling back to generating thumbnail from source)", eventID, err)
	} else {
		return createThumbnail(thumbnail, png)
	}
	return createThumbnail(original, png)
}

func (portal *Portal) convertWebPtoPNG(webpImage []byte) ([]byte, error) {
	webpDecoded, err := webp.Decode(bytes.NewReader(webpImage))
	if err != nil {
		return nil, fmt.Errorf("failed to decode webp image: %w", err)
	}

	var pngBuffer bytes.Buffer
	if err = png.Encode(&pngBuffer, webpDecoded); err != nil {
		return nil, fmt.Errorf("failed to encode png image: %w", err)
	}

	return pngBuffer.Bytes(), nil
}

type PaddedImage struct {
	image.Image
	Size    int
	OffsetX int
	OffsetY int
}

func (img *PaddedImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, img.Size, img.Size)
}

func (img *PaddedImage) At(x, y int) color.Color {
	return img.Image.At(x+img.OffsetX, y+img.OffsetY)
}

func (portal *Portal) convertToWebP(img []byte) ([]byte, error) {
	decodedImg, _, err := image.Decode(bytes.NewReader(img))
	if err != nil {
		return img, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := decodedImg.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width != height {
		paddedImg := &PaddedImage{
			Image:   decodedImg,
			OffsetX: bounds.Min.Y,
			OffsetY: bounds.Min.X,
		}
		if width > height {
			paddedImg.Size = width
			paddedImg.OffsetY -= (paddedImg.Size - height) / 2
		} else {
			paddedImg.Size = height
			paddedImg.OffsetX -= (paddedImg.Size - width) / 2
		}
		decodedImg = paddedImg
	}

	var webpBuffer bytes.Buffer
	if err = webp.Encode(&webpBuffer, decodedImg, nil); err != nil {
		return img, fmt.Errorf("failed to encode webp image: %w", err)
	}

	return webpBuffer.Bytes(), nil
}

func (portal *Portal) preprocessMatrixMedia(ctx context.Context, sender *User, relaybotFormatted bool, content *event.MessageEventContent, eventID id.EventID, mediaType whatsmeow.MediaType) (*MediaUpload, error) {
	fileName := content.Body
	var caption string
	var mentionedJIDs []string
	var hasHTMLCaption bool
	isSticker := string(content.MsgType) == event.EventSticker.Type
	if content.FileName != "" && content.Body != content.FileName {
		fileName = content.FileName
		caption = content.Body
		hasHTMLCaption = content.Format == event.FormatHTML
	}
	if relaybotFormatted || hasHTMLCaption {
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
		return nil, err
	}
	data, err := portal.MainIntent().DownloadBytesContext(ctx, mxc)
	if err != nil {
		return nil, util.NewDualError(errMediaDownloadFailed, err)
	}
	if file != nil {
		err = file.DecryptInPlace(data)
		if err != nil {
			return nil, util.NewDualError(errMediaDecryptFailed, err)
		}
	}
	mimeType := content.GetInfo().MimeType
	var convertErr error
	// Allowed mime types from https://developers.facebook.com/docs/whatsapp/on-premises/reference/media
	switch {
	case isSticker:
		if mimeType != "image/webp" || content.Info.Width != content.Info.Height {
			data, convertErr = portal.convertToWebP(data)
			content.Info.MimeType = "image/webp"
		}
	case mediaType == whatsmeow.MediaVideo:
		switch mimeType {
		case "video/mp4", "video/3gpp":
			// Allowed
		case "image/gif":
			data, convertErr = ffmpeg.ConvertBytes(ctx, data, ".mp4", []string{"-f", "gif"}, []string{
				"-pix_fmt", "yuv420p", "-c:v", "libx264", "-movflags", "+faststart",
				"-filter:v", "crop='floor(in_w/2)*2:floor(in_h/2)*2'",
			}, mimeType)
			content.Info.MimeType = "video/mp4"
		case "video/webm":
			data, convertErr = ffmpeg.ConvertBytes(ctx, data, ".mp4", []string{"-f", "webm"}, []string{
				"-pix_fmt", "yuv420p", "-c:v", "libx264",
			}, mimeType)
			content.Info.MimeType = "video/mp4"
		default:
			return nil, fmt.Errorf("%w %q in video message", errMediaUnsupportedType, mimeType)
		}
	case mediaType == whatsmeow.MediaImage:
		switch mimeType {
		case "image/jpeg", "image/png":
			// Allowed
		case "image/webp":
			data, convertErr = portal.convertWebPtoPNG(data)
			content.Info.MimeType = "image/png"
		default:
			return nil, fmt.Errorf("%w %q in image message", errMediaUnsupportedType, mimeType)
		}
	case mediaType == whatsmeow.MediaAudio:
		switch mimeType {
		case "audio/aac", "audio/mp4", "audio/amr", "audio/mpeg", "audio/ogg; codecs=opus":
			// Allowed
		case "audio/ogg":
			// Hopefully it's opus already
			content.Info.MimeType = "audio/ogg; codecs=opus"
		default:
			return nil, fmt.Errorf("%w %q in audio message", errMediaUnsupportedType, mimeType)
		}
	case mediaType == whatsmeow.MediaDocument:
		// Everything is allowed
	}
	if convertErr != nil {
		if content.Info.MimeType != mimeType || data == nil {
			return nil, util.NewDualError(fmt.Errorf("%w (%s to %s)", errMediaConvertFailed, mimeType, content.Info.MimeType), convertErr)
		} else {
			// If the mime type didn't change and the errored conversion function returned the original data, just log a warning and continue
			portal.log.Warnfln("Failed to re-encode %s media: %v, continuing with original file", mimeType, convertErr)
		}
	}
	uploadResp, err := sender.Client.Upload(ctx, data, mediaType)
	if err != nil {
		return nil, util.NewDualError(errMediaWhatsAppUploadFailed, err)
	}

	// Audio doesn't have thumbnails
	var thumbnail []byte
	if mediaType != whatsmeow.MediaAudio {
		thumbnail, err = portal.downloadThumbnail(ctx, data, content.GetInfo().ThumbnailURL, eventID, isSticker)
		// Ignore format errors for non-image files, we don't care about those thumbnails
		if err != nil && (!errors.Is(err, image.ErrFormat) || mediaType == whatsmeow.MediaImage) {
			portal.log.Warnfln("Failed to generate thumbnail for %s: %v", eventID, err)
		}
	}

	return &MediaUpload{
		UploadResponse: uploadResp,
		FileName:       fileName,
		Caption:        caption,
		MentionedJIDs:  mentionedJIDs,
		Thumbnail:      thumbnail,
		FileLength:     len(data),
	}, nil
}

type MediaUpload struct {
	whatsmeow.UploadResponse
	Caption       string
	FileName      string
	MentionedJIDs []string
	Thumbnail     []byte
	FileLength    int
}

func (portal *Portal) addRelaybotFormat(sender *User, content *event.MessageEventContent) bool {
	member := portal.MainIntent().Member(portal.MXID, sender.MXID)
	if member == nil {
		member = &event.MemberEventContent{}
	}
	content.EnsureHasHTML()
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

var (
	TypeMSC3381PollStart      = event.Type{Class: event.MessageEventType, Type: "org.matrix.msc3381.poll.start"}
	TypeMSC3381PollResponse   = event.Type{Class: event.MessageEventType, Type: "org.matrix.msc3381.poll.response"}
	TypeMSC3381V2PollResponse = event.Type{Class: event.MessageEventType, Type: "org.matrix.msc3381.v2.poll.response"}
)

type PollResponseContent struct {
	RelatesTo  event.RelatesTo `json:"m.relates_to"`
	V1Response struct {
		Answers []string `json:"answers"`
	} `json:"org.matrix.msc3381.poll.response"`
	V2Selections []string `json:"org.matrix.msc3381.v2.selections"`
}

func (content *PollResponseContent) GetRelatesTo() *event.RelatesTo {
	return &content.RelatesTo
}

func (content *PollResponseContent) OptionalGetRelatesTo() *event.RelatesTo {
	if content.RelatesTo.Type == "" {
		return nil
	}
	return &content.RelatesTo
}

func (content *PollResponseContent) SetRelatesTo(rel *event.RelatesTo) {
	content.RelatesTo = *rel
}

type MSC1767Message struct {
	Text    string `json:"org.matrix.msc1767.text,omitempty"`
	HTML    string `json:"org.matrix.msc1767.html,omitempty"`
	Message []struct {
		MimeType string `json:"mimetype"`
		Body     string `json:"body"`
	} `json:"org.matrix.msc1767.message,omitempty"`
}

func (portal *Portal) msc1767ToWhatsApp(msg MSC1767Message, mentions bool) (string, []string) {
	for _, part := range msg.Message {
		if part.MimeType == "text/html" && msg.HTML == "" {
			msg.HTML = part.Body
		} else if part.MimeType == "text/plain" && msg.Text == "" {
			msg.Text = part.Body
		}
	}
	if msg.HTML != "" {
		if mentions {
			return portal.bridge.Formatter.ParseMatrix(msg.HTML)
		} else {
			return portal.bridge.Formatter.ParseMatrixWithoutMentions(msg.HTML), nil
		}
	}
	return msg.Text, nil
}

type PollStartContent struct {
	RelatesTo *event.RelatesTo `json:"m.relates_to"`
	PollStart struct {
		Kind          string         `json:"kind"`
		MaxSelections int            `json:"max_selections"`
		Question      MSC1767Message `json:"question"`
		Answers       []struct {
			ID string `json:"id"`
			MSC1767Message
		} `json:"answers"`
	} `json:"org.matrix.msc3381.poll.start"`
}

func (content *PollStartContent) GetRelatesTo() *event.RelatesTo {
	if content.RelatesTo == nil {
		content.RelatesTo = &event.RelatesTo{}
	}
	return content.RelatesTo
}

func (content *PollStartContent) OptionalGetRelatesTo() *event.RelatesTo {
	return content.RelatesTo
}

func (content *PollStartContent) SetRelatesTo(rel *event.RelatesTo) {
	content.RelatesTo = rel
}

func init() {
	event.TypeMap[TypeMSC3381PollResponse] = reflect.TypeOf(PollResponseContent{})
	event.TypeMap[TypeMSC3381V2PollResponse] = reflect.TypeOf(PollResponseContent{})
	event.TypeMap[TypeMSC3381PollStart] = reflect.TypeOf(PollStartContent{})
}

func (portal *Portal) convertMatrixPollVote(_ context.Context, sender *User, evt *event.Event) (*waProto.Message, *User, *extraConvertMeta, error) {
	content, ok := evt.Content.Parsed.(*PollResponseContent)
	if !ok {
		return nil, sender, nil, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed)
	}
	var answers []string
	if content.V1Response.Answers != nil {
		answers = content.V1Response.Answers
	} else if content.V2Selections != nil {
		answers = content.V2Selections
	}
	pollMsg := portal.bridge.DB.Message.GetByMXID(content.RelatesTo.EventID)
	if pollMsg == nil {
		return nil, sender, nil, errTargetNotFound
	}
	pollMsgInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     portal.Key.JID,
			Sender:   pollMsg.Sender,
			IsFromMe: pollMsg.Sender.User == sender.JID.User,
			IsGroup:  portal.IsGroupChat(),
		},
		ID:   pollMsg.JID,
		Type: "poll",
	}
	optionHashes := make([][]byte, 0, len(answers))
	if pollMsg.Type == database.MsgMatrixPoll {
		mappedAnswers := pollMsg.GetPollOptionHashes(answers)
		for _, selection := range answers {
			hash, ok := mappedAnswers[selection]
			if ok {
				optionHashes = append(optionHashes, hash[:])
			} else {
				portal.log.Warnfln("Didn't find hash for option %s in %s's vote to %s", selection, evt.Sender, pollMsg.MXID)
			}
		}
	} else {
		for _, selection := range answers {
			hash, _ := hex.DecodeString(selection)
			if hash != nil && len(hash) == 32 {
				optionHashes = append(optionHashes, hash)
			}
		}
	}
	pollUpdate, err := sender.Client.EncryptPollVote(pollMsgInfo, &waProto.PollVoteMessage{
		SelectedOptions: optionHashes,
	})
	return &waProto.Message{PollUpdateMessage: pollUpdate}, sender, nil, err
}

func (portal *Portal) convertMatrixPollStart(_ context.Context, sender *User, evt *event.Event) (*waProto.Message, *User, *extraConvertMeta, error) {
	content, ok := evt.Content.Parsed.(*PollStartContent)
	if !ok {
		return nil, sender, nil, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed)
	}
	maxAnswers := content.PollStart.MaxSelections
	if maxAnswers >= len(content.PollStart.Answers) || maxAnswers < 0 {
		maxAnswers = 0
	}
	fmt.Printf("%+v\n", content.PollStart)
	ctxInfo := portal.generateContextInfo(content.RelatesTo)
	var question string
	question, ctxInfo.MentionedJid = portal.msc1767ToWhatsApp(content.PollStart.Question, true)
	if len(question) == 0 {
		return nil, sender, nil, errPollMissingQuestion
	}
	options := make([]*waProto.PollCreationMessage_Option, len(content.PollStart.Answers))
	optionMap := make(map[[32]byte]string, len(options))
	for i, opt := range content.PollStart.Answers {
		body, _ := portal.msc1767ToWhatsApp(opt.MSC1767Message, false)
		hash := sha256.Sum256([]byte(body))
		if _, alreadyExists := optionMap[hash]; alreadyExists {
			portal.log.Warnfln("Poll %s by %s has option %q more than once, rejecting", evt.ID, evt.Sender, body)
			return nil, sender, nil, errPollDuplicateOption
		}
		optionMap[hash] = opt.ID
		options[i] = &waProto.PollCreationMessage_Option{
			OptionName: proto.String(body),
		}
	}
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	return &waProto.Message{
		PollCreationMessage: &waProto.PollCreationMessage{
			Name:                   proto.String(question),
			Options:                options,
			SelectableOptionsCount: proto.Uint32(uint32(maxAnswers)),
			ContextInfo:            ctxInfo,
		},
		MessageContextInfo: &waProto.MessageContextInfo{
			MessageSecret: secret,
		},
	}, sender, &extraConvertMeta{PollOptions: optionMap}, err
}

func (portal *Portal) generateContextInfo(relatesTo *event.RelatesTo) *waProto.ContextInfo {
	var ctxInfo waProto.ContextInfo
	replyToID := relatesTo.GetReplyTo()
	if len(replyToID) > 0 {
		replyToMsg := portal.bridge.DB.Message.GetByMXID(replyToID)
		if replyToMsg != nil && !replyToMsg.IsFakeJID() && (replyToMsg.Type == database.MsgNormal || replyToMsg.Type == database.MsgMatrixPoll) {
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
	return &ctxInfo
}

type extraConvertMeta struct {
	PollOptions map[[32]byte]string
	EditRootMsg *database.Message
}

func getEditError(rootMsg *database.Message, editer *User) error {
	switch {
	case rootMsg == nil:
		return errEditUnknownTarget
	case rootMsg.Type != database.MsgNormal || rootMsg.IsFakeJID():
		return errEditUnknownTargetType
	case rootMsg.Sender.User != editer.JID.User:
		return errEditDifferentSender
	case time.Since(rootMsg.Timestamp) > whatsmeow.EditWindow:
		return errEditTooOld
	default:
		return nil
	}
}

func (portal *Portal) convertMatrixMessage(ctx context.Context, sender *User, evt *event.Event) (*waProto.Message, *User, *extraConvertMeta, error) {
	if evt.Type == TypeMSC3381PollResponse || evt.Type == TypeMSC3381V2PollResponse {
		return portal.convertMatrixPollVote(ctx, sender, evt)
	} else if evt.Type == TypeMSC3381PollStart {
		return portal.convertMatrixPollStart(ctx, sender, evt)
	}
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil, sender, nil, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed)
	}
	extraMeta := &extraConvertMeta{}
	var editRootMsg *database.Message
	if editEventID := content.RelatesTo.GetReplaceID(); editEventID != "" {
		editRootMsg = portal.bridge.DB.Message.GetByMXID(editEventID)
		if editErr := getEditError(editRootMsg, sender); editErr != nil {
			return nil, sender, extraMeta, editErr
		}
		extraMeta.EditRootMsg = editRootMsg
		if content.NewContent != nil {
			content = content.NewContent
		}
	}

	msg := &waProto.Message{}
	ctxInfo := portal.generateContextInfo(content.RelatesTo)
	relaybotFormatted := false
	if !sender.IsLoggedIn() || (portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User) {
		if !portal.HasRelaybot() {
			return nil, sender, extraMeta, errUserNotLoggedIn
		}
		relaybotFormatted = portal.addRelaybotFormat(sender, content)
		sender = portal.GetRelayUser()
	}
	if evt.Type == event.EventSticker {
		if relaybotFormatted {
			// Stickers can't have captions, so force relaybot stickers to be images
			content.MsgType = event.MsgImage
		} else {
			content.MsgType = event.MessageType(event.EventSticker.Type)
		}
	}
	if content.MsgType == event.MsgImage && content.GetInfo().MimeType == "image/gif" {
		content.MsgType = event.MsgVideo
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		text := content.Body
		if content.MsgType == event.MsgNotice && !portal.bridge.Config.Bridge.BridgeNotices {
			return nil, sender, extraMeta, errMNoticeDisabled
		}
		if content.Format == event.FormatHTML {
			text, ctxInfo.MentionedJid = portal.bridge.Formatter.ParseMatrix(content.FormattedBody)
		}
		if content.MsgType == event.MsgEmote && !relaybotFormatted {
			text = "/me " + text
		}
		msg.ExtendedTextMessage = &waProto.ExtendedTextMessage{
			Text:        &text,
			ContextInfo: ctxInfo,
		}
		hasPreview := portal.convertURLPreviewToWhatsApp(ctx, sender, evt, msg.ExtendedTextMessage)
		if ctx.Err() != nil {
			return nil, sender, extraMeta, ctx.Err()
		}
		if ctxInfo.StanzaId == nil && ctxInfo.MentionedJid == nil && ctxInfo.Expiration == nil && !hasPreview {
			// No need for extended message
			msg.ExtendedTextMessage = nil
			msg.Conversation = &text
		}
	case event.MsgImage:
		media, err := portal.preprocessMatrixMedia(ctx, sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaImage)
		if media == nil {
			return nil, sender, extraMeta, err
		}
		ctxInfo.MentionedJid = media.MentionedJIDs
		msg.ImageMessage = &waProto.ImageMessage{
			ContextInfo:   ctxInfo,
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			DirectPath:    &media.DirectPath,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
	case event.MessageType(event.EventSticker.Type):
		media, err := portal.preprocessMatrixMedia(ctx, sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaImage)
		if media == nil {
			return nil, sender, extraMeta, err
		}
		ctxInfo.MentionedJid = media.MentionedJIDs
		msg.StickerMessage = &waProto.StickerMessage{
			ContextInfo:   ctxInfo,
			PngThumbnail:  media.Thumbnail,
			Url:           &media.URL,
			DirectPath:    &media.DirectPath,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
	case event.MsgVideo:
		gifPlayback := content.GetInfo().MimeType == "image/gif"
		media, err := portal.preprocessMatrixMedia(ctx, sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaVideo)
		if media == nil {
			return nil, sender, extraMeta, err
		}
		duration := uint32(content.GetInfo().Duration / 1000)
		ctxInfo.MentionedJid = media.MentionedJIDs
		msg.VideoMessage = &waProto.VideoMessage{
			ContextInfo:   ctxInfo,
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			DirectPath:    &media.DirectPath,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			GifPlayback:   &gifPlayback,
			Seconds:       &duration,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
	case event.MsgAudio:
		media, err := portal.preprocessMatrixMedia(ctx, sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaAudio)
		if media == nil {
			return nil, sender, extraMeta, err
		}
		duration := uint32(content.GetInfo().Duration / 1000)
		msg.AudioMessage = &waProto.AudioMessage{
			ContextInfo:   ctxInfo,
			Url:           &media.URL,
			DirectPath:    &media.DirectPath,
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
		media, err := portal.preprocessMatrixMedia(ctx, sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaDocument)
		if media == nil {
			return nil, sender, extraMeta, err
		}
		msg.DocumentMessage = &waProto.DocumentMessage{
			ContextInfo:   ctxInfo,
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			DirectPath:    &media.DirectPath,
			Title:         &media.FileName,
			FileName:      &media.FileName,
			MediaKey:      media.MediaKey,
			Mimetype:      &content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    proto.Uint64(uint64(media.FileLength)),
		}
		if media.Caption != "" {
			msg.DocumentWithCaptionMessage = &waProto.FutureProofMessage{
				Message: &waProto.Message{
					DocumentMessage: msg.DocumentMessage,
				},
			}
			msg.DocumentMessage = nil
		}
	case event.MsgLocation:
		lat, long, err := parseGeoURI(content.GeoURI)
		if err != nil {
			return nil, sender, extraMeta, fmt.Errorf("%w: %v", errInvalidGeoURI, err)
		}
		msg.LocationMessage = &waProto.LocationMessage{
			DegreesLatitude:  &lat,
			DegreesLongitude: &long,
			Comment:          &content.Body,
			ContextInfo:      ctxInfo,
		}
	default:
		return nil, sender, extraMeta, fmt.Errorf("%w %q", errUnknownMsgType, content.MsgType)
	}

	if editRootMsg != nil {
		msg = &waProto.Message{
			EditedMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{
					ProtocolMessage: &waProto.ProtocolMessage{
						Key: &waProto.MessageKey{
							FromMe:    proto.Bool(true),
							Id:        proto.String(editRootMsg.JID),
							RemoteJid: proto.String(portal.Key.JID.String()),
						},
						Type:          waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
						EditedMessage: msg,
						TimestampMs:   proto.Int64(evt.Timestamp),
					},
				},
			},
		}
	}

	return msg, sender, extraMeta, nil
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

func (portal *Portal) HandleMatrixMessage(sender *User, evt *event.Event, timings messageTimings) {
	start := time.Now()
	ms := metricSender{portal: portal, timings: &timings}

	allowRelay := evt.Type != TypeMSC3381PollResponse && evt.Type != TypeMSC3381V2PollResponse && evt.Type != TypeMSC3381PollStart
	if err := portal.canBridgeFrom(sender, allowRelay); err != nil {
		go ms.sendMessageMetrics(evt, err, "Ignoring", true)
		return
	} else if portal.Key.JID == types.StatusBroadcastJID && portal.bridge.Config.Bridge.DisableStatusBroadcastSend {
		go ms.sendMessageMetrics(evt, errBroadcastSendDisabled, "Ignoring", true)
		return
	}

	messageAge := timings.totalReceive
	origEvtID := evt.ID
	var dbMsg *database.Message
	if retryMeta := evt.Content.AsMessage().MessageSendRetry; retryMeta != nil {
		origEvtID = retryMeta.OriginalEventID
		dbMsg = portal.bridge.DB.Message.GetByMXID(origEvtID)
		if dbMsg != nil && dbMsg.Sent {
			portal.log.Debugfln("Ignoring retry request %s (#%d, age: %s) for %s/%s from %s as message was already sent", evt.ID, retryMeta.RetryCount, messageAge, origEvtID, dbMsg.JID, evt.Sender)
			go ms.sendMessageMetrics(evt, nil, "", true)
			return
		} else if dbMsg != nil {
			portal.log.Debugfln("Got retry request %s (#%d, age: %s) for %s/%s from %s", evt.ID, retryMeta.RetryCount, messageAge, origEvtID, dbMsg.JID, evt.Sender)
		} else {
			portal.log.Debugfln("Got retry request %s (#%d, age: %s) for %s from %s (original message not known)", evt.ID, retryMeta.RetryCount, messageAge, origEvtID, evt.Sender)
		}
	} else {
		portal.log.Debugfln("Received message %s from %s (age: %s)", evt.ID, evt.Sender, messageAge)
	}

	errorAfter := portal.bridge.Config.Bridge.MessageHandlingTimeout.ErrorAfter
	deadline := portal.bridge.Config.Bridge.MessageHandlingTimeout.Deadline
	isScheduled, _ := evt.Content.Raw["com.beeper.scheduled"].(bool)
	if isScheduled {
		portal.log.Debugfln("%s is a scheduled message, extending handling timeouts", evt.ID)
		errorAfter *= 10
		deadline *= 10
	}

	if errorAfter > 0 {
		remainingTime := errorAfter - messageAge
		if remainingTime < 0 {
			go ms.sendMessageMetrics(evt, errTimeoutBeforeHandling, "Timeout handling", true)
			return
		} else if remainingTime < 1*time.Second {
			portal.log.Warnfln("Message %s was delayed before reaching the bridge, only have %s (of %s timeout) until delay warning", evt.ID, remainingTime, errorAfter)
		}
		go func() {
			time.Sleep(remainingTime)
			ms.sendMessageMetrics(evt, errMessageTakingLong, "Timeout handling", false)
		}()
	}

	ctx := context.Background()
	if deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}

	timings.preproc = time.Since(start)
	start = time.Now()
	msg, sender, extraMeta, err := portal.convertMatrixMessage(ctx, sender, evt)
	timings.convert = time.Since(start)
	if msg == nil {
		go ms.sendMessageMetrics(evt, err, "Error converting", true)
		return
	}
	dbMsgType := database.MsgNormal
	if msg.PollCreationMessage != nil || msg.PollCreationMessageV2 != nil {
		dbMsgType = database.MsgMatrixPoll
	} else if msg.EditedMessage == nil {
		portal.MarkDisappearing(nil, origEvtID, time.Duration(portal.ExpirationTime)*time.Second, time.Now())
	} else {
		dbMsgType = database.MsgEdit
	}
	info := portal.generateMessageInfo(sender)
	if dbMsg == nil {
		dbMsg = portal.markHandled(nil, nil, info, evt.ID, false, true, dbMsgType, database.MsgNoError)
	} else {
		info.ID = dbMsg.JID
	}
	if dbMsgType == database.MsgMatrixPoll && extraMeta != nil && extraMeta.PollOptions != nil {
		dbMsg.PutPollOptions(extraMeta.PollOptions)
	}
	portal.log.Debugln("Sending event", evt.ID, "to WhatsApp", info.ID)
	start = time.Now()
	resp, err := sender.Client.SendMessage(ctx, portal.Key.JID, msg, whatsmeow.SendRequestExtra{ID: info.ID})
	timings.totalSend = time.Since(start)
	timings.whatsmeow = resp.DebugTimings
	go ms.sendMessageMetrics(evt, err, "Error sending", true)
	if err == nil {
		dbMsg.MarkSent(resp.Timestamp)
	}
}

func (portal *Portal) HandleMatrixReaction(sender *User, evt *event.Event) {
	if err := portal.canBridgeFrom(sender, false); err != nil {
		go portal.sendMessageMetrics(evt, err, "Ignoring", nil)
		return
	} else if portal.Key.JID.Server == types.BroadcastServer {
		// TODO implement this, probably by only sending the reaction to the sender of the status message?
		//      (whatsapp hasn't published the feature yet)
		go portal.sendMessageMetrics(evt, errBroadcastReactionNotSupported, "Ignoring", nil)
		return
	}

	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if ok && strings.Contains(content.RelatesTo.Key, "retry") || strings.HasPrefix(content.RelatesTo.Key, "\u267b") { // ♻️
		if retryRequested, _ := portal.requestMediaRetry(sender, content.RelatesTo.EventID, nil); retryRequested {
			_, _ = portal.MainIntent().RedactEvent(portal.MXID, evt.ID, mautrix.ReqRedact{
				Reason: "requested media from phone",
			})
			// Errored media, don't try to send as reaction
			return
		}
	}

	portal.log.Debugfln("Received reaction event %s from %s", evt.ID, evt.Sender)
	err := portal.handleMatrixReaction(sender, evt)
	go portal.sendMessageMetrics(evt, err, "Error sending", nil)
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
	portal.upsertReaction(nil, nil, target.JID, sender.JID, evt.ID, info.ID)
	portal.log.Debugln("Sending reaction", evt.ID, "to WhatsApp", info.ID)
	resp, err := portal.sendReactionToWhatsApp(sender, info.ID, target, content.RelatesTo.Key, evt.Timestamp)
	if err == nil {
		dbMsg.MarkSent(resp.Timestamp)
	}
	return err
}

func (portal *Portal) sendReactionToWhatsApp(sender *User, id types.MessageID, target *database.Message, key string, timestamp int64) (whatsmeow.SendResponse, error) {
	var messageKeyParticipant *string
	if !portal.IsPrivateChat() {
		messageKeyParticipant = proto.String(target.Sender.ToNonAD().String())
	}
	key = variationselector.Remove(key)
	return sender.Client.SendMessage(context.TODO(), portal.Key.JID, &waProto.Message{
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
	}, whatsmeow.SendRequestExtra{ID: id})
}

func (portal *Portal) upsertReaction(txn dbutil.Transaction, intent *appservice.IntentAPI, targetJID types.MessageID, senderJID types.JID, mxid id.EventID, jid types.MessageID) {
	dbReaction := portal.bridge.DB.Reaction.GetByTargetJID(portal.Key, targetJID, senderJID)
	if dbReaction == nil {
		dbReaction = portal.bridge.DB.Reaction.New()
		dbReaction.Chat = portal.Key
		dbReaction.TargetJID = targetJID
		dbReaction.Sender = senderJID
	} else if intent != nil {
		portal.log.Debugfln("Redacting old Matrix reaction %s after new one (%s) was sent", dbReaction.MXID, mxid)
		var err error
		if intent != nil {
			_, err = intent.RedactEvent(portal.MXID, dbReaction.MXID)
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
	dbReaction.Upsert(txn)
}

func (portal *Portal) HandleMatrixRedaction(sender *User, evt *event.Event) {
	if err := portal.canBridgeFrom(sender, true); err != nil {
		go portal.sendMessageMetrics(evt, err, "Ignoring", nil)
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
		go portal.sendMessageMetrics(evt, errTargetNotFound, "Ignoring", nil)
	} else if msg.IsFakeJID() {
		go portal.sendMessageMetrics(evt, errTargetIsFake, "Ignoring", nil)
	} else if portal.Key.JID == types.StatusBroadcastJID && portal.bridge.Config.Bridge.DisableStatusBroadcastSend {
		go portal.sendMessageMetrics(evt, errBroadcastSendDisabled, "Ignoring", nil)
	} else if msg.Type == database.MsgReaction {
		if msg.Sender.User != sender.JID.User {
			go portal.sendMessageMetrics(evt, errReactionSentBySomeoneElse, "Ignoring", nil)
		} else if reaction := portal.bridge.DB.Reaction.GetByMXID(evt.Redacts); reaction == nil {
			go portal.sendMessageMetrics(evt, errReactionDatabaseNotFound, "Ignoring", nil)
		} else if reactionTarget := reaction.GetTarget(); reactionTarget == nil {
			go portal.sendMessageMetrics(evt, errReactionTargetNotFound, "Ignoring", nil)
		} else {
			portal.log.Debugfln("Sending redaction reaction %s of %s/%s to WhatsApp", evt.ID, msg.MXID, msg.JID)
			_, err := portal.sendReactionToWhatsApp(sender, "", reactionTarget, "", evt.Timestamp)
			go portal.sendMessageMetrics(evt, err, "Error sending", nil)
		}
	} else {
		key := &waProto.MessageKey{
			FromMe:    proto.Bool(true),
			Id:        proto.String(msg.JID),
			RemoteJid: proto.String(portal.Key.JID.String()),
		}
		if msg.Sender.User != sender.JID.User {
			if portal.IsPrivateChat() {
				go portal.sendMessageMetrics(evt, errDMSentByOtherUser, "Ignoring", nil)
				return
			}
			key.FromMe = proto.Bool(false)
			key.Participant = proto.String(msg.Sender.ToNonAD().String())
		}
		portal.log.Debugfln("Sending redaction %s of %s/%s to WhatsApp", evt.ID, msg.MXID, msg.JID)
		_, err := sender.Client.SendMessage(context.TODO(), portal.Key.JID, &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_REVOKE.Enum(),
				Key:  key,
			},
		})
		go portal.sendMessageMetrics(evt, err, "Error sending", nil)
	}
}

func (portal *Portal) HandleMatrixReadReceipt(sender bridge.User, eventID id.EventID, receipt event.ReadReceipt) {
	portal.handleMatrixReadReceipt(sender.(*User), eventID, receipt.Timestamp, true)
}

func (portal *Portal) handleMatrixReadReceipt(sender *User, eventID id.EventID, receiptTimestamp time.Time, isExplicit bool) {
	if !sender.IsLoggedIn() {
		if isExplicit {
			portal.log.Debugfln("Ignoring read receipt by %s/%s: user is not connected to WhatsApp", sender.MXID, sender.JID)
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

func (portal *Portal) canBridgeFrom(sender *User, allowRelay bool) error {
	if !sender.IsLoggedIn() {
		if allowRelay && portal.HasRelaybot() {
			return nil
		} else if sender.Session != nil {
			return errUserNotConnected
		} else {
			return errUserNotLoggedIn
		}
	} else if portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User && (!allowRelay || !portal.HasRelaybot()) {
		return errDifferentUser
	}
	return nil
}

func (portal *Portal) resetChildSpaceStatus() {
	for _, childPortal := range portal.bridge.portalsByJID {
		if childPortal.ParentGroup == portal.Key.JID {
			if portal.MXID != "" && childPortal.InSpace {
				go childPortal.removeSpaceParentEvent(portal.MXID)
			}
			childPortal.InSpace = false
			if childPortal.parentPortal == portal {
				childPortal.parentPortal = nil
			}
		}
	}
}

func (portal *Portal) Delete() {
	portal.Portal.Delete()
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByJID, portal.Key)
	if len(portal.MXID) > 0 {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.resetChildSpaceStatus()
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
	intent := portal.MainIntent()
	if portal.bridge.SpecVersions.UnstableFeatures["com.beeper.room_yeeting"] {
		err := intent.BeeperDeleteRoom(portal.MXID)
		if err == nil || errors.Is(err, mautrix.MNotFound) {
			return
		}
		portal.log.Warnfln("Failed to delete %s using hungryserv yeet endpoint, falling back to normal behavior: %v", portal.MXID, err)
	}
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

func (portal *Portal) HandleMatrixLeave(brSender bridge.User) {
	sender := brSender.(*User)
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

func (portal *Portal) HandleMatrixKick(brSender bridge.User, brTarget bridge.Ghost) {
	sender := brSender.(*User)
	target := brTarget.(*Puppet)
	_, err := sender.Client.UpdateGroupParticipants(portal.Key.JID, map[types.JID]whatsmeow.ParticipantChange{
		target.JID: whatsmeow.ParticipantChangeRemove,
	})
	if err != nil {
		portal.log.Errorfln("Failed to kick %s from group as %s: %v", target.JID, sender.MXID, err)
		return
	}
	//portal.log.Infoln("Kick %s response: %s", puppet.JID, <-resp)
}

func (portal *Portal) HandleMatrixInvite(brSender bridge.User, brTarget bridge.Ghost) {
	sender := brSender.(*User)
	target := brTarget.(*Puppet)
	_, err := sender.Client.UpdateGroupParticipants(portal.Key.JID, map[types.JID]whatsmeow.ParticipantChange{
		target.JID: whatsmeow.ParticipantChangeAdd,
	})
	if err != nil {
		portal.log.Errorfln("Failed to add %s to group as %s: %v", target.JID, sender.MXID, err)
		return
	}
	//portal.log.Infofln("Add %s response: %s", puppet.JID, <-resp)
}

func (portal *Portal) HandleMatrixMeta(brSender bridge.User, evt *event.Event) {
	sender := brSender.(*User)
	if !sender.Whitelisted || !sender.IsLoggedIn() {
		return
	}

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
