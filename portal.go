// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"maps"
	"math"
	"mime"
	"net/http"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
	"go.mau.fi/util/exzerolog"
	cwebp "go.mau.fi/webp"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"golang.org/x/exp/slices"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/util/exerrors"
	"go.mau.fi/util/exmime"
	"go.mau.fi/util/ffmpeg"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/random"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

const StatusBroadcastTopic = "WhatsApp status updates from your contacts"
const StatusBroadcastName = "WhatsApp Status Broadcast"
const BroadcastTopic = "WhatsApp broadcast list"
const UnnamedBroadcastName = "Unnamed broadcast list"
const PrivateChatTopic = "WhatsApp private chat"

var ErrStatusBroadcastDisabled = errors.New("status bridging is disabled")

func (br *WABridge) GetPortalByMXID(mxid id.RoomID) *Portal {
	ctx := context.TODO()
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByMXID[mxid]
	if !ok {
		dbPortal, err := br.DB.Portal.GetByMXID(ctx, mxid)
		if err != nil {
			br.ZLog.Err(err).Stringer("mxid", mxid).Msg("Failed to get portal by MXID")
			return nil
		}
		return br.loadDBPortal(ctx, dbPortal, nil)
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
	err := portal.Update(context.TODO())
	if err != nil {
		portal.zlog.Err(err).Msg("Failed to mark portal as encrypted")
	}
}

func (portal *Portal) ReceiveMatrixEvent(user bridge.User, evt *event.Event) {
	if user.GetPermissionLevel() >= bridgeconfig.PermissionLevelUser || portal.HasRelaybot() {
		portal.events <- &PortalEvent{
			MatrixMessage: &PortalMatrixMessage{
				user:       user.(*User),
				evt:        evt,
				receivedAt: time.Now(),
			},
		}
	}
}

func (br *WABridge) GetPortalByJID(key database.PortalKey) *Portal {
	ctx := context.TODO()
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByJID[key]
	if !ok {
		dbPortal, err := br.DB.Portal.GetByJID(ctx, key)
		if err != nil {
			br.ZLog.Err(err).Str("key", key.String()).Msg("Failed to get portal by JID")
			return nil
		}
		return br.loadDBPortal(ctx, dbPortal, &key)
	}
	return portal
}

func (br *WABridge) GetExistingPortalByJID(key database.PortalKey) *Portal {
	ctx := context.TODO()
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	portal, ok := br.portalsByJID[key]
	if !ok {
		dbPortal, err := br.DB.Portal.GetByJID(ctx, key)
		if err != nil {
			br.ZLog.Err(err).Str("key", key.String()).Msg("Failed to get portal by JID")
			return nil
		}
		return br.loadDBPortal(ctx, dbPortal, nil)
	}
	return portal
}

func (br *WABridge) GetAllPortals() []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAll(context.TODO()))
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
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByJID(context.TODO(), jid))
}

func (br *WABridge) GetAllByParentGroup(jid types.JID) []*Portal {
	return br.dbPortalsToPortals(br.DB.Portal.GetAllByParentGroup(context.TODO(), jid))
}

func (br *WABridge) dbPortalsToPortals(dbPortals []*database.Portal, err error) []*Portal {
	if err != nil {
		br.ZLog.Err(err).Msg("Failed to get portals")
		return nil
	}
	br.portalsLock.Lock()
	defer br.portalsLock.Unlock()
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		if dbPortal == nil {
			continue
		}
		portal, ok := br.portalsByJID[dbPortal.Key]
		if !ok {
			portal = br.loadDBPortal(context.TODO(), dbPortal, nil)
		}
		output[index] = portal
	}
	return output
}

func (br *WABridge) loadDBPortal(ctx context.Context, dbPortal *database.Portal, key *database.PortalKey) *Portal {
	if dbPortal == nil {
		if key == nil {
			return nil
		}
		dbPortal = br.DB.Portal.New()
		dbPortal.Key = *key
		err := dbPortal.Insert(ctx)
		if err != nil {
			br.ZLog.Err(err).Str("key", key.String()).Msg("Failed to insert new portal")
			return nil
		}
	}
	portal := br.NewPortal(dbPortal)
	br.portalsByJID[portal.Key] = portal
	if len(portal.MXID) > 0 {
		br.portalsByMXID[portal.MXID] = portal
	}
	return portal
}

func (portal *Portal) GetUsers() []*User {
	// TODO what's this for?
	return nil
}

func (br *WABridge) NewManualPortal(key database.PortalKey) *Portal {
	dbPortal := br.DB.Portal.New()
	dbPortal.Key = key
	return br.NewPortal(dbPortal)
}

func (br *WABridge) NewPortal(dbPortal *database.Portal) *Portal {
	portal := &Portal{
		Portal:          dbPortal,
		bridge:          br,
		events:          make(chan *PortalEvent, br.Config.Bridge.PortalMessageBuffer),
		mediaErrorCache: make(map[types.MessageID]*FailedMediaMeta),
	}
	portal.updateLogger()
	go portal.handleMessageLoop()
	return portal
}

func (portal *Portal) updateLogger() {
	logWith := portal.bridge.ZLog.With().Stringer("portal_key", portal.Key)
	if portal.MXID != "" {
		logWith = logWith.Stringer("room_id", portal.MXID)
	}
	portal.zlog = logWith.Logger()
}

const recentlyHandledLength = 100

type fakeMessage struct {
	Sender    types.JID
	Text      string
	ID        string
	Time      time.Time
	Important bool
}

type PortalEvent struct {
	Message       *PortalMessage
	MatrixMessage *PortalMatrixMessage
	MediaRetry    *PortalMediaRetry
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
	zlog   zerolog.Logger

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

	events chan *PortalEvent

	mediaErrorCache map[types.MessageID]*FailedMediaMeta

	galleryCache          []*event.MessageEventContent
	galleryCacheRootEvent id.EventID
	galleryCacheStart     time.Time
	galleryCacheReplyTo   *ReplyInfo
	galleryCacheSender    types.JID

	currentlySleepingToDelete sync.Map

	relayUser    *User
	parentPortal *Portal
}

const GalleryMaxTime = 10 * time.Minute

func (portal *Portal) stopGallery() {
	if portal.galleryCache != nil {
		portal.galleryCache = nil
		portal.galleryCacheSender = types.EmptyJID
		portal.galleryCacheReplyTo = nil
		portal.galleryCacheStart = time.Time{}
		portal.galleryCacheRootEvent = ""
	}
}

func (portal *Portal) startGallery(evt *events.Message, msg *ConvertedMessage) {
	portal.galleryCache = []*event.MessageEventContent{msg.Content}
	portal.galleryCacheSender = evt.Info.Sender.ToNonAD()
	portal.galleryCacheReplyTo = msg.ReplyTo
	portal.galleryCacheStart = time.Now()
}

func (portal *Portal) extendGallery(msg *ConvertedMessage) int {
	portal.galleryCache = append(portal.galleryCache, msg.Content)
	msg.Content = &event.MessageEventContent{
		MsgType:             event.MsgBeeperGallery,
		Body:                "Sent a gallery",
		BeeperGalleryImages: portal.galleryCache,
	}
	msg.Content.SetEdit(portal.galleryCacheRootEvent)
	// Don't set the gallery images in the edit fallback
	msg.Content.BeeperGalleryImages = nil
	return len(portal.galleryCache) - 1
}

var (
	_ bridge.Portal                    = (*Portal)(nil)
	_ bridge.ReadReceiptHandlingPortal = (*Portal)(nil)
	_ bridge.MembershipHandlingPortal  = (*Portal)(nil)
	_ bridge.MetaHandlingPortal        = (*Portal)(nil)
	_ bridge.TypingPortal              = (*Portal)(nil)
)

func (portal *Portal) handleWhatsAppMessageLoopItem(msg *PortalMessage) {
	log := portal.zlog.With().
		Str("action", "handle whatsapp event").
		Stringer("source_user_jid", msg.source.JID).
		Stringer("source_user_mxid", msg.source.MXID).
		Logger()
	ctx := log.WithContext(context.TODO())
	if len(portal.MXID) == 0 {
		if msg.fake == nil && msg.undecryptable == nil && (msg.evt == nil || !containsSupportedMessage(msg.evt.Message)) {
			log.Debug().Msg("Not creating portal room for incoming message: message is not a chat message")
			return
		}
		log.Debug().Msg("Creating Matrix room from incoming message")
		err := portal.CreateMatrixRoom(ctx, msg.source, nil, nil, false, true)
		if err != nil {
			log.Err(err).Msg("Failed to create portal room")
			return
		}
	}
	portal.latestEventBackfillLock.Lock()
	defer portal.latestEventBackfillLock.Unlock()
	switch {
	case msg.evt != nil:
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.
				Str("message_id", msg.evt.Info.ID).
				Stringer("message_sender", msg.evt.Info.Sender)
		})
		portal.handleMessage(ctx, msg.source, msg.evt, false)
	case msg.receipt != nil:
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("receipt_type", msg.receipt.Type.GoString())
		})
		portal.handleReceipt(ctx, msg.receipt, msg.source)
	case msg.undecryptable != nil:
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.
				Str("message_id", msg.undecryptable.Info.ID).
				Stringer("message_sender", msg.undecryptable.Info.Sender).
				Bool("undecryptable", true)
		})
		portal.stopGallery()
		portal.handleUndecryptableMessage(ctx, msg.source, msg.undecryptable)
	case msg.fake != nil:
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.
				Str("fake_message_id", msg.fake.ID).
				Stringer("message_sender", msg.fake.Sender)
		})
		portal.stopGallery()
		msg.fake.ID = "FAKE::" + msg.fake.ID
		portal.handleFakeMessage(ctx, *msg.fake)
	default:
		log.Warn().Any("event_data", msg).Msg("Unexpected PortalMessage with no message")
	}
}

func (portal *Portal) handleMatrixMessageLoopItem(msg *PortalMatrixMessage) {
	log := portal.zlog.With().
		Str("action", "handle matrix event").
		Stringer("event_id", msg.evt.ID).
		Str("event_type", msg.evt.Type.Type).
		Stringer("sender", msg.evt.Sender).
		Logger()
	ctx := log.WithContext(context.TODO())
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
	portal.handleMatrixReadReceipt(ctx, msg.user, "", evtTS, false)
	timings.implicitRR = time.Since(implicitRRStart)
	switch msg.evt.Type {
	case event.EventMessage, event.EventSticker, TypeMSC3381V2PollResponse, TypeMSC3381PollResponse, TypeMSC3381PollStart:
		portal.HandleMatrixMessage(ctx, msg.user, msg.evt, timings)
	case event.EventRedaction:
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Stringer("redaction_target_mxid", msg.evt.Redacts)
		})
		portal.HandleMatrixRedaction(ctx, msg.user, msg.evt)
	case event.EventReaction:
		portal.HandleMatrixReaction(ctx, msg.user, msg.evt)
	default:
		log.Warn().Msg("Unsupported event type in portal message channel")
	}
}

func (portal *Portal) handleDeliveryReceipt(ctx context.Context, receipt *events.Receipt, source *User) {
	if !portal.IsPrivateChat() {
		return
	}
	log := zerolog.Ctx(ctx)
	for _, msgID := range receipt.MessageIDs {
		msg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, msgID)
		if err != nil {
			log.Err(err).Str("message_id", msgID).Msg("Failed to get receipt target message")
			continue
		} else if msg == nil || msg.IsFakeMXID() {
			continue
		}
		if msg.Sender == source.JID {
			portal.bridge.SendRawMessageCheckpoint(&status.MessageCheckpoint{
				EventID:    msg.MXID,
				RoomID:     portal.MXID,
				Step:       status.MsgStepRemote,
				Timestamp:  jsontime.UM(receipt.Timestamp),
				Status:     status.MsgStatusDelivered,
				ReportedBy: status.MsgReportedByBridge,
			})
			portal.sendStatusEvent(ctx, msg.MXID, "", nil, &[]id.UserID{portal.MainIntent().UserID})
		}
	}
}

func (portal *Portal) handleReceipt(ctx context.Context, receipt *events.Receipt, source *User) {
	if receipt.Sender.Server != types.DefaultUserServer {
		// TODO handle lids
		return
	}
	if receipt.Type == types.ReceiptTypeDelivered {
		portal.handleDeliveryReceipt(ctx, receipt, source)
		return
	}
	// The order of the message ID array depends on the sender's platform, so we just have to find
	// the last message based on timestamp. Also, timestamps only have second precision, so if
	// there are many messages at the same second just mark them all as read, because we don't
	// know which one is last
	markAsRead := make([]*database.Message, 0, 1)
	var bestTimestamp time.Time
	log := zerolog.Ctx(ctx)
	for _, msgID := range receipt.MessageIDs {
		msg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, msgID)
		if err != nil {
			log.Err(err).Str("message_id", msgID).Msg("Failed to get receipt target message")
		} else if msg == nil || msg.IsFakeMXID() {
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
			source.SetLastReadTS(ctx, portal.Key, markAsRead[0].Timestamp)
		} else {
			source.SetLastReadTS(ctx, portal.Key, receipt.Timestamp)
		}
	}
	intent := portal.bridge.GetPuppetByJID(receipt.Sender).IntentFor(portal)
	for _, msg := range markAsRead {
		err := intent.SetReadMarkers(ctx, portal.MXID, source.makeReadMarkerContent(msg.MXID, intent.IsCustomPuppet))
		if err != nil {
			log.Err(err).
				Stringer("message_mxid", msg.MXID).
				Stringer("read_by_user_mxid", intent.UserID).
				Msg("Failed to mark message as read")
		} else {
			log.Debug().
				Stringer("message_mxid", msg.MXID).
				Stringer("read_by_user_mxid", intent.UserID).
				Msg("Marked message as read")
		}
	}
}

func (portal *Portal) handleMessageLoop() {
	for {
		portal.handleOneMessageLoopItem()
	}
}

func (portal *Portal) handleOneMessageLoopItem() {
	defer func() {
		if err := recover(); err != nil {
			logEvt := portal.zlog.WithLevel(zerolog.FatalLevel).
				Str(zerolog.ErrorStackFieldName, string(debug.Stack()))
			actualErr, ok := err.(error)
			if ok {
				logEvt = logEvt.Err(actualErr)
			} else {
				logEvt = logEvt.Any(zerolog.ErrorFieldName, err)
			}
			logEvt.Msg("Portal message handler panicked")
		}
	}()
	select {
	case msg := <-portal.events:
		if msg.Message != nil {
			portal.handleWhatsAppMessageLoopItem(msg.Message)
		} else if msg.MatrixMessage != nil {
			portal.handleMatrixMessageLoopItem(msg.MatrixMessage)
		} else if msg.MediaRetry != nil {
			portal.handleMediaRetry(msg.MediaRetry.evt, msg.MediaRetry.source)
		} else {
			portal.zlog.Warn().Msg("Unexpected PortalEvent with no data")
		}
	}
}

func containsSupportedMessage(waMsg *waProto.Message) bool {
	if waMsg == nil {
		return false
	}
	return waMsg.Conversation != nil || waMsg.ExtendedTextMessage != nil || waMsg.ImageMessage != nil ||
		waMsg.StickerMessage != nil || waMsg.AudioMessage != nil || waMsg.VideoMessage != nil || waMsg.PtvMessage != nil ||
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
	case waMsg.PtvMessage != nil:
		return fmt.Sprintf("round video %s", waMsg.GetPtvMessage().GetMimetype())
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
	case waMsg.PollCreationMessage != nil || waMsg.PollCreationMessageV2 != nil || waMsg.PollCreationMessageV3 != nil:
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

func (portal *Portal) convertMessage(ctx context.Context, intent *appservice.IntentAPI, source *User, info *types.MessageInfo, waMsg *waProto.Message, isBackfill bool) *ConvertedMessage {
	switch {
	case waMsg.Conversation != nil || waMsg.ExtendedTextMessage != nil:
		return portal.convertTextMessage(ctx, intent, source, waMsg)
	case waMsg.TemplateMessage != nil:
		return portal.convertTemplateMessage(ctx, intent, source, info, waMsg.GetTemplateMessage())
	case waMsg.HighlyStructuredMessage != nil:
		return portal.convertTemplateMessage(ctx, intent, source, info, waMsg.GetHighlyStructuredMessage().GetHydratedHsm())
	case waMsg.TemplateButtonReplyMessage != nil:
		return portal.convertTemplateButtonReplyMessage(ctx, intent, waMsg.GetTemplateButtonReplyMessage())
	case waMsg.ListMessage != nil:
		return portal.convertListMessage(ctx, intent, source, waMsg.GetListMessage())
	case waMsg.ListResponseMessage != nil:
		return portal.convertListResponseMessage(ctx, intent, waMsg.GetListResponseMessage())
	case waMsg.PollCreationMessage != nil:
		return portal.convertPollCreationMessage(ctx, intent, waMsg.GetPollCreationMessage())
	case waMsg.PollCreationMessageV2 != nil:
		return portal.convertPollCreationMessage(ctx, intent, waMsg.GetPollCreationMessageV2())
	case waMsg.PollCreationMessageV3 != nil:
		return portal.convertPollCreationMessage(ctx, intent, waMsg.GetPollCreationMessageV3())
	case waMsg.PollUpdateMessage != nil:
		return portal.convertPollUpdateMessage(ctx, intent, source, info, waMsg.GetPollUpdateMessage())
	case waMsg.ImageMessage != nil:
		return portal.convertMediaMessage(ctx, intent, source, info, waMsg.GetImageMessage(), "photo", isBackfill)
	case waMsg.StickerMessage != nil:
		return portal.convertMediaMessage(ctx, intent, source, info, waMsg.GetStickerMessage(), "sticker", isBackfill)
	case waMsg.VideoMessage != nil:
		return portal.convertMediaMessage(ctx, intent, source, info, waMsg.GetVideoMessage(), "video attachment", isBackfill)
	case waMsg.PtvMessage != nil:
		return portal.convertMediaMessage(ctx, intent, source, info, waMsg.GetPtvMessage(), "video message", isBackfill)
	case waMsg.AudioMessage != nil:
		typeName := "audio attachment"
		if waMsg.GetAudioMessage().GetPtt() {
			typeName = "voice message"
		}
		return portal.convertMediaMessage(ctx, intent, source, info, waMsg.GetAudioMessage(), typeName, isBackfill)
	case waMsg.DocumentMessage != nil:
		return portal.convertMediaMessage(ctx, intent, source, info, waMsg.GetDocumentMessage(), "file attachment", isBackfill)
	case waMsg.ContactMessage != nil:
		return portal.convertContactMessage(ctx, intent, waMsg.GetContactMessage())
	case waMsg.ContactsArrayMessage != nil:
		return portal.convertContactsArrayMessage(ctx, intent, waMsg.GetContactsArrayMessage())
	case waMsg.LocationMessage != nil:
		return portal.convertLocationMessage(ctx, intent, waMsg.GetLocationMessage())
	case waMsg.LiveLocationMessage != nil:
		return portal.convertLiveLocationMessage(ctx, intent, waMsg.GetLiveLocationMessage())
	case waMsg.GroupInviteMessage != nil:
		return portal.convertGroupInviteMessage(ctx, intent, info, waMsg.GetGroupInviteMessage())
	case waMsg.ProtocolMessage != nil && waMsg.ProtocolMessage.GetType() == waProto.ProtocolMessage_EPHEMERAL_SETTING:
		portal.ExpirationTime = waMsg.ProtocolMessage.GetEphemeralExpiration()
		err := portal.Update(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to save portal after updating expiration timer")
		}
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

func (portal *Portal) implicitlyEnableDisappearingMessages(ctx context.Context, timer time.Duration) {
	portal.ExpirationTime = uint32(timer.Seconds())
	err := portal.Update(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to save portal after implicitly enabling disappearing timer")
	}
	intent := portal.MainIntent()
	if portal.Encrypted {
		intent = portal.bridge.Bot
	}
	duration := formatDuration(time.Duration(portal.ExpirationTime) * time.Second)
	_, err = portal.sendMessage(ctx, intent, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("Automatically enabled disappearing message timer (%s) because incoming message is disappearing", duration),
	}, nil, 0)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to send notice about implicit disappearing timer")
	}
}

func (portal *Portal) UpdateGroupDisappearingMessages(ctx context.Context, sender *types.JID, timestamp time.Time, timer uint32) {
	if portal.ExpirationTime == timer {
		return
	}
	portal.ExpirationTime = timer
	err := portal.Update(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to save portal after updating expiration timer")
	}
	intent := portal.MainIntent()
	if sender != nil && sender.Server == types.DefaultUserServer {
		intent = portal.bridge.GetPuppetByJID(sender.ToNonAD()).IntentFor(portal)
	} else {
		sender = &types.EmptyJID
	}
	_, err = portal.sendMessage(ctx, intent, event.EventMessage, &event.MessageEventContent{
		Body:    portal.formatDisappearingMessageNotice(),
		MsgType: event.MsgNotice,
	}, nil, timestamp.UnixMilli())
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).
			Uint32("new_timer", timer).
			Stringer("sender_jid", sender).
			Msg("Failed to notify portal about disappearing message timer change")
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

func (portal *Portal) handleUndecryptableMessage(ctx context.Context, source *User, evt *events.UndecryptableMessage) {
	log := zerolog.Ctx(ctx)
	if len(portal.MXID) == 0 {
		log.Warn().Msg("handleUndecryptableMessage called even though portal.MXID is empty")
		return
	} else if portal.isRecentlyHandled(evt.Info.ID, database.MsgErrDecryptionFailed) {
		log.Debug().Msg("Not handling recently handled message")
		return
	} else if existingMsg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, evt.Info.ID); err != nil {
		log.Err(err).Msg("Failed to get message from database to check if undecryptable message is duplicate")
		return
	} else if existingMsg != nil {
		log.Debug().Msg("Not handling duplicate message")
		return
	}
	metricType := "error"
	if evt.IsUnavailable {
		metricType = "unavailable"
	}
	Analytics.Track(source.MXID, "WhatsApp undecryptable message", map[string]interface{}{
		"messageID":         evt.Info.ID,
		"undecryptableType": metricType,
	})
	intent := portal.getMessageIntent(ctx, source, &evt.Info)
	if intent == nil {
		return
	}
	content := undecryptableMessageContent
	resp, err := portal.sendMessage(ctx, intent, event.EventMessage, &content, nil, evt.Info.Timestamp.UnixMilli())
	if err != nil {
		log.Err(err).Msg("Failed to send WhatsApp decryption error message to Matrix")
		return
	}
	portal.finishHandling(ctx, nil, &evt.Info, resp.EventID, intent.UserID, database.MsgUnknown, 0, database.MsgErrDecryptionFailed)
}

func (portal *Portal) handleFakeMessage(ctx context.Context, msg fakeMessage) {
	log := zerolog.Ctx(ctx)
	if portal.isRecentlyHandled(msg.ID, database.MsgNoError) {
		log.Debug().Msg("Not handling recently handled message")
		return
	} else if existingMsg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, msg.ID); err != nil {
		log.Err(err).Msg("Failed to get message from database to check if fake message is duplicate")
		return
	} else if existingMsg != nil {
		log.Debug().Msg("Not handling duplicate message")
		return
	}
	if msg.Sender.Server != types.DefaultUserServer {
		log.Debug().Msg("Not handling message from @lid user")
		// TODO handle lids
		return
	}
	intent := portal.bridge.GetPuppetByJID(msg.Sender).IntentFor(portal)
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && msg.Sender.User == portal.Key.Receiver.User && portal.Key.Receiver != portal.Key.JID {
		log.Debug().Msg("Not handling fake message for user who doesn't have double puppeting enabled")
		return
	}
	msgType := event.MsgNotice
	if msg.Important {
		msgType = event.MsgText
	}
	resp, err := portal.sendMessage(ctx, intent, event.EventMessage, &event.MessageEventContent{
		MsgType: msgType,
		Body:    msg.Text,
	}, nil, msg.Time.UnixMilli())
	if err != nil {
		log.Err(err).Msg("Failed to send fake message to Matrix")
	} else {
		portal.finishHandling(ctx, nil, &types.MessageInfo{
			ID:        msg.ID,
			Timestamp: msg.Time,
			MessageSource: types.MessageSource{
				Sender: msg.Sender,
			},
		}, resp.EventID, intent.UserID, database.MsgFake, 0, database.MsgNoError)
	}
}

func (portal *Portal) handleMessage(ctx context.Context, source *User, evt *events.Message, historical bool) {
	log := zerolog.Ctx(ctx)
	if len(portal.MXID) == 0 {
		log.Warn().Msg("handleMessage called even though portal.MXID is empty")
		return
	}
	msgID := evt.Info.ID
	msgType := getMessageType(evt.Message)
	if msgType == "ignore" {
		return
	} else if portal.isRecentlyHandled(msgID, database.MsgNoError) {
		log.Debug().Msg("Not handling recently handled message")
		return
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("wa_message_type", msgType)
	})
	existingMsg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, msgID)
	if err != nil {
		log.Err(err).Msg("Failed to get message from database to check if message is duplicate")
		return
	}
	if existingMsg != nil {
		if existingMsg.Error == database.MsgErrDecryptionFailed {
			resolveType := "sender"
			if evt.UnavailableRequestID != "" {
				resolveType = "phone"
			}
			Analytics.Track(source.MXID, "WhatsApp undecryptable message resolved", map[string]interface{}{
				"messageID":   evt.Info.ID,
				"resolveType": resolveType,
			})
			log.Debug().Str("resolved_via", resolveType).Msg("Got decryptable version of previously undecryptable message")
		} else {
			log.Debug().Msg("Not handling duplicate message")
			return
		}
	}
	var editTargetMsg *database.Message
	if msgType == "edit" {
		editTargetID := evt.Message.GetProtocolMessage().GetKey().GetId()
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("edit_target_id", editTargetID)
		})
		editTargetMsg, err = portal.bridge.DB.Message.GetByJID(ctx, portal.Key, editTargetID)
		if err != nil {
			log.Err(err).Msg("Failed to get edit target message from database")
			return
		} else if editTargetMsg == nil {
			log.Warn().Msg("Not handling edit: couldn't find edit target")
			return
		} else if editTargetMsg.Type != database.MsgNormal {
			log.Warn().Str("edit_target_db_type", string(editTargetMsg.Type)).
				Msg("Not handling edit: edit target is not a normal message")
			return
		} else if editTargetMsg.Sender.User != evt.Info.Sender.User {
			log.Warn().Stringer("edit_target_sender", editTargetMsg.Sender).
				Msg("Not handling edit: edit was sent by another user")
			return
		}
		evt.Message = evt.Message.GetProtocolMessage().GetEditedMessage()
	}

	intent := portal.getMessageIntent(ctx, source, &evt.Info)
	if intent == nil {
		return
	}
	converted := portal.convertMessage(ctx, intent, source, &evt.Info, evt.Message, false)
	if converted != nil {
		isGalleriable := portal.bridge.Config.Bridge.BeeperGalleries &&
			(evt.Message.ImageMessage != nil || evt.Message.VideoMessage != nil) &&
			(portal.galleryCache == nil ||
				(evt.Info.Sender.ToNonAD() == portal.galleryCacheSender &&
					converted.ReplyTo.Equals(portal.galleryCacheReplyTo) &&
					time.Since(portal.galleryCacheStart) < GalleryMaxTime)) &&
			// Captions aren't allowed in galleries (this needs to be checked before the caption is merged)
			converted.Caption == nil &&
			// Images can't be edited
			editTargetMsg == nil

		if !historical && portal.IsPrivateChat() && evt.Info.Sender.Device == 0 && converted.ExpiresIn > 0 && portal.ExpirationTime == 0 {
			log.Info().
				Str("timer", converted.ExpiresIn.String()).
				Msg("Implicitly enabling disappearing messages as incoming message is disappearing")
			portal.implicitlyEnableDisappearingMessages(ctx, converted.ExpiresIn)
		}
		if evt.Info.IsIncomingBroadcast() {
			if converted.Extra == nil {
				converted.Extra = map[string]any{}
			}
			converted.Extra["fi.mau.whatsapp.source_broadcast_list"] = evt.Info.Chat.String()
		}
		if portal.bridge.Config.Bridge.CaptionInMessage {
			converted.MergeCaption()
		}
		var eventID id.EventID
		var lastEventID id.EventID
		if existingMsg != nil {
			portal.MarkDisappearing(ctx, existingMsg.MXID, converted.ExpiresIn, evt.Info.Timestamp)
			converted.Content.SetEdit(existingMsg.MXID)
		} else if converted.ReplyTo != nil {
			portal.SetReply(ctx, converted.Content, converted.ReplyTo, false)
		}
		dbMsgType := database.MsgNormal
		if editTargetMsg != nil {
			dbMsgType = database.MsgEdit
			converted.Content.SetEdit(editTargetMsg.MXID)
		}
		galleryStarted := false
		var galleryPart int
		if isGalleriable {
			if portal.galleryCache == nil {
				portal.startGallery(evt, converted)
				galleryStarted = true
			} else {
				galleryPart = portal.extendGallery(converted)
				dbMsgType = database.MsgBeeperGallery
			}
		} else if editTargetMsg == nil {
			// Stop collecting a gallery (except if it's an edit)
			portal.stopGallery()
		}
		var resp *mautrix.RespSendEvent
		resp, err = portal.sendMessage(ctx, converted.Intent, converted.Type, converted.Content, converted.Extra, evt.Info.Timestamp.UnixMilli())
		if err != nil {
			log.Err(err).Msg("Failed to send WhatsApp message to Matrix")
		} else {
			if editTargetMsg == nil {
				portal.MarkDisappearing(ctx, resp.EventID, converted.ExpiresIn, evt.Info.Timestamp)
			}
			eventID = resp.EventID
			lastEventID = eventID
			if galleryStarted {
				portal.galleryCacheRootEvent = eventID
			} else if galleryPart != 0 {
				eventID = portal.galleryCacheRootEvent
			}
		}
		// TODO figure out how to handle captions with undecryptable messages turning decryptable
		if converted.Caption != nil && existingMsg == nil && editTargetMsg == nil {
			resp, err = portal.sendMessage(ctx, converted.Intent, converted.Type, converted.Caption, nil, evt.Info.Timestamp.UnixMilli())
			if err != nil {
				log.Err(err).Msg("Failed to send caption of WhatsApp message to Matrix")
			} else {
				portal.MarkDisappearing(ctx, resp.EventID, converted.ExpiresIn, evt.Info.Timestamp)
				lastEventID = resp.EventID
			}
		}
		if converted.MultiEvent != nil && existingMsg == nil && editTargetMsg == nil {
			for index, subEvt := range converted.MultiEvent {
				resp, err = portal.sendMessage(ctx, converted.Intent, converted.Type, subEvt, nil, evt.Info.Timestamp.UnixMilli())
				if err != nil {
					log.Err(err).Int("part_number", index+1).Msg("Failed to send sub-event of WhatsApp message to Matrix")
				} else {
					portal.MarkDisappearing(ctx, resp.EventID, converted.ExpiresIn, evt.Info.Timestamp)
					lastEventID = resp.EventID
				}
			}
		}
		if source.MXID == intent.UserID && portal.bridge.Config.Homeserver.Software != bridgeconfig.SoftwareHungry {
			// There are some edge cases (like call notices) where previous messages aren't marked as read
			// when the user sends a message from another device, so just mark the new message as read to be safe.
			// Hungryserv does this automatically, so the bridge doesn't need to do it manually.
			err = intent.SetReadMarkers(ctx, portal.MXID, source.makeReadMarkerContent(lastEventID, true))
			if err != nil {
				log.Warn().Err(err).Stringer("last_event_id", lastEventID).
					Msg("Failed to mark last message as read after sending")
			}
		}
		if len(eventID) != 0 {
			portal.finishHandling(ctx, existingMsg, &evt.Info, eventID, intent.UserID, dbMsgType, galleryPart, converted.Error)
		}
	} else if msgType == "reaction" || msgType == "encrypted reaction" {
		if evt.Message.GetEncReactionMessage() != nil {
			log.UpdateContext(func(c zerolog.Context) zerolog.Context {
				return c.Str("reaction_target_id", evt.Message.GetEncReactionMessage().GetTargetMessageKey().GetId())
			})
			decryptedReaction, err := source.Client.DecryptReaction(evt)
			if err != nil {
				log.Err(err).Msg("Failed to decrypt reaction")
			} else {
				portal.HandleMessageReaction(ctx, intent, source, &evt.Info, decryptedReaction, existingMsg)
			}
		} else {
			portal.HandleMessageReaction(ctx, intent, source, &evt.Info, evt.Message.GetReactionMessage(), existingMsg)
		}
	} else if msgType == "revoke" {
		portal.HandleMessageRevoke(ctx, source, &evt.Info, evt.Message.GetProtocolMessage().GetKey())
		if existingMsg != nil {
			_, _ = portal.MainIntent().RedactEvent(ctx, portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
				Reason: "The undecryptable message was actually the deletion of another message",
			})
			err = existingMsg.UpdateMXID(ctx, "net.maunium.whatsapp.fake::"+existingMsg.MXID, database.MsgFake, database.MsgNoError)
			if err != nil {
				log.Err(err).Msg("Failed to update message in database after finding undecryptable message was a revoke message")
			}
		}
	} else {
		log.Warn().Any("event_info", evt.Info).Msg("Unhandled message")
		if existingMsg != nil {
			_, _ = portal.MainIntent().RedactEvent(ctx, portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
				Reason: "The undecryptable message contained an unsupported message type",
			})
			err = existingMsg.UpdateMXID(ctx, "net.maunium.whatsapp.fake::"+existingMsg.MXID, database.MsgFake, database.MsgNoError)
			if err != nil {
				log.Err(err).Msg("Failed to update message in database after finding undecryptable message was an unknown message")
			}
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

func (portal *Portal) markHandled(ctx context.Context, msg *database.Message, info *types.MessageInfo, mxid id.EventID, senderMXID id.UserID, isSent, recent bool, msgType database.MessageType, galleryPart int, errType database.MessageErrorType) *database.Message {
	if msg == nil {
		msg = portal.bridge.DB.Message.New()
		msg.Chat = portal.Key
		msg.JID = info.ID
		msg.MXID = mxid
		msg.GalleryPart = galleryPart
		msg.Timestamp = info.Timestamp
		msg.Sender = info.Sender
		msg.SenderMXID = senderMXID
		msg.Sent = isSent
		msg.Type = msgType
		msg.Error = errType
		if info.IsIncomingBroadcast() {
			msg.BroadcastListJID = info.Chat
		}
		err := msg.Insert(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to insert message to database")
		}
	} else {
		err := msg.UpdateMXID(ctx, mxid, msgType, errType)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to update message in database")
		}
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

func (portal *Portal) getMessagePuppet(ctx context.Context, user *User, info *types.MessageInfo) (puppet *Puppet) {
	if info.IsFromMe {
		return portal.bridge.GetPuppetByJID(user.JID)
	} else if portal.IsPrivateChat() {
		puppet = portal.bridge.GetPuppetByJID(portal.Key.JID)
	} else if !info.Sender.IsEmpty() {
		puppet = portal.bridge.GetPuppetByJID(info.Sender)
	}
	if puppet == nil {
		zerolog.Ctx(ctx).Warn().Msg("Message doesn't seem to have a valid sender: puppet is nil")
		return nil
	}
	user.EnqueuePortalResync(portal)
	puppet.SyncContact(ctx, user, true, true, "handling message")
	return puppet
}

func (portal *Portal) getMessageIntent(ctx context.Context, user *User, info *types.MessageInfo) *appservice.IntentAPI {
	if portal.IsNewsletter() && info.Sender == info.Chat {
		return portal.MainIntent()
	}
	puppet := portal.getMessagePuppet(ctx, user, info)
	if puppet == nil {
		return nil
	}
	intent := puppet.IntentFor(portal)
	if !intent.IsCustomPuppet && portal.IsPrivateChat() && info.Sender.User == portal.Key.Receiver.User && portal.Key.Receiver != portal.Key.JID {
		zerolog.Ctx(ctx).Debug().Msg("Not handling message: user doesn't have double puppeting enabled")
		return nil
	}
	return intent
}

func (portal *Portal) finishHandling(ctx context.Context, existing *database.Message, message *types.MessageInfo, mxid id.EventID, senderMXID id.UserID, msgType database.MessageType, galleryPart int, errType database.MessageErrorType) {
	portal.markHandled(ctx, existing, message, mxid, senderMXID, true, true, msgType, galleryPart, errType)
	portal.sendDeliveryReceipt(ctx, mxid)
	logEvt := zerolog.Ctx(ctx).Debug().
		Stringer("matrix_event_id", mxid)
	if errType != database.MsgNoError {
		logEvt.Str("error_type", string(errType))
	}
	logEvt.Msg("Successfully handled WhatsApp message")
}

func (portal *Portal) kickExtraUsers(ctx context.Context, participantMap map[types.JID]bool) {
	members, err := portal.MainIntent().JoinedMembers(ctx, portal.MXID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get member list to kick extra users")
		return
	}
	for member := range members.Joined {
		jid, ok := portal.bridge.ParsePuppetMXID(member)
		if ok {
			_, shouldBePresent := participantMap[jid]
			if !shouldBePresent {
				_, err = portal.MainIntent().KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{
					UserID: member,
					Reason: "User had left this WhatsApp chat",
				})
				if err != nil {
					zerolog.Ctx(ctx).Warn().Err(err).
						Stringer("user_id", member).
						Msg("Failed to kick extra user from room")
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

func (portal *Portal) syncParticipant(ctx context.Context, source *User, participant types.GroupParticipant, puppet *Puppet, user *User, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		if err := recover(); err != nil {
			zerolog.Ctx(ctx).Error().
				Bytes(zerolog.ErrorStackFieldName, debug.Stack()).
				Any(zerolog.ErrorFieldName, err).
				Stringer("participant_jid", participant.JID).
				Msg("Syncing participant panicked")
		}
	}()
	puppet.SyncContact(ctx, source, true, false, "group participant")
	if portal.MXID != "" {
		if user != nil && user != source {
			portal.ensureUserInvited(ctx, user)
		}
		if user == nil || !puppet.IntentFor(portal).IsCustomPuppet {
			err := puppet.IntentFor(portal).EnsureJoined(ctx, portal.MXID)
			if err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).
					Stringer("participant_jid", participant.JID).
					Msg("Failed to make ghost user join portal")
			}
		}
	}
}

func (portal *Portal) SyncParticipants(ctx context.Context, source *User, metadata *types.GroupInfo) ([]id.UserID, *event.PowerLevelsEventContent) {
	if portal.IsNewsletter() {
		return nil, nil
	}
	changed := false
	var levels *event.PowerLevelsEventContent
	var err error
	if portal.MXID != "" {
		levels, err = portal.MainIntent().PowerLevels(ctx, portal.MXID)
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
	log := zerolog.Ctx(ctx)
	for _, participant := range metadata.Participants {
		if participant.JID.IsEmpty() || participant.JID.Server != types.DefaultUserServer {
			wg.Done()
			// TODO handle lids
			continue
		}
		log.Debug().
			Stringer("participant_jid", participant.JID).
			Bool("is_admin", participant.IsAdmin).
			Msg("Syncing participant")
		participantMap[participant.JID] = true
		puppet := portal.bridge.GetPuppetByJID(participant.JID)
		user := portal.bridge.GetUserByJID(participant.JID)
		if portal.bridge.Config.Bridge.ParallelMemberSync {
			go portal.syncParticipant(ctx, source, participant, puppet, user, &wg)
		} else {
			portal.syncParticipant(ctx, source, participant, puppet, user, &wg)
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
			_, err = portal.MainIntent().SetPowerLevels(ctx, portal.MXID, levels)
			if err != nil {
				log.Err(err).Msg("Failed to update power levels in room")
			}
		}
		portal.kickExtraUsers(ctx, participantMap)
	}
	wg.Wait()
	log.Debug().Msg("Participant sync completed")
	return userIDs, levels
}

func reuploadAvatar(ctx context.Context, intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
	getResp, err := http.DefaultClient.Get(url)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}
	data, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to read avatar bytes: %w", err)
	}

	resp, err := intent.UploadBytes(ctx, data, http.DetectContentType(data))
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}
	return resp.ContentURI, nil
}

func (user *User) reuploadAvatarDirectPath(ctx context.Context, intent *appservice.IntentAPI, directPath string) (id.ContentURI, error) {
	data, err := user.Client.DownloadMediaWithPath(directPath, nil, nil, nil, 0, "", "")
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}
	resp, err := intent.UploadBytes(ctx, data, http.DetectContentType(data))
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}
	return resp.ContentURI, nil
}

func (user *User) updateAvatar(ctx context.Context, jid types.JID, isCommunity bool, avatarID *string, avatarURL *id.ContentURI, avatarSet *bool, intent *appservice.IntentAPI) bool {
	currentID := ""
	if *avatarSet && *avatarID != "remove" && *avatarID != "unauthorized" {
		currentID = *avatarID
	}
	avatar, err := user.Client.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{
		Preview:     false,
		ExistingID:  currentID,
		IsCommunity: isCommunity,
	})
	log := zerolog.Ctx(ctx)
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
		log.Err(err).Msg("Failed to get avatar URL")
		return false
	} else if avatar == nil {
		// Avatar hasn't changed
		return false
	}
	if avatar.ID == *avatarID && *avatarSet {
		return false
	} else if len(avatar.URL) == 0 && len(avatar.DirectPath) == 0 {
		log.Warn().Msg("Didn't get URL in response to avatar query")
		return false
	} else if avatar.ID != *avatarID || avatarURL.IsEmpty() {
		var url id.ContentURI
		if len(avatar.URL) > 0 {
			url, err = reuploadAvatar(ctx, intent, avatar.URL)
			if err != nil {
				log.Err(err).Msg("Failed to reupload avatar")
				return false
			}
		} else {
			url, err = user.reuploadAvatarDirectPath(ctx, intent, avatar.DirectPath)
			if err != nil {
				log.Err(err).Msg("Failed to reupload avatar")
				return false
			}
		}
		*avatarURL = url
	}
	log.Debug().Str("old_avatar_id", *avatarID).Str("new_avatar_id", avatar.ID).Msg("Updated avatar")
	*avatarID = avatar.ID
	*avatarSet = false
	return true
}

func (portal *Portal) UpdateNewsletterAvatar(ctx context.Context, user *User, meta *types.NewsletterMetadata) bool {
	portal.avatarLock.Lock()
	defer portal.avatarLock.Unlock()
	var picID string
	picture := meta.ThreadMeta.Picture
	if picture == nil {
		picID = meta.ThreadMeta.Preview.ID
	} else {
		picID = picture.ID
	}
	if picID == "" {
		picID = "remove"
	}
	if portal.Avatar == picID && portal.AvatarSet {
		return false
	}
	log := zerolog.Ctx(ctx)
	if picID == "remove" {
		portal.AvatarURL = id.ContentURI{}
	} else if portal.Avatar != picID || portal.AvatarURL.IsEmpty() {
		var err error
		if picture == nil {
			meta, err = user.Client.GetNewsletterInfo(portal.Key.JID)
			if err != nil {
				log.Err(err).Msg("Failed to fetch full res avatar info for newsletter")
				return false
			}
			picture = meta.ThreadMeta.Picture
			if picture == nil {
				log.Warn().Msg("Didn't get full res avatar info for newsletter")
				return false
			}
			picID = picture.ID
		}
		portal.AvatarURL, err = user.reuploadAvatarDirectPath(ctx, portal.MainIntent(), picture.DirectPath)
		if err != nil {
			log.Err(err).Msg("Failed to reupload newsletter avatar")
			return false
		}
	}
	portal.Avatar = picID
	portal.AvatarSet = false
	return portal.setRoomAvatar(ctx, true, types.EmptyJID, true)
}

func (portal *Portal) UpdateAvatar(ctx context.Context, user *User, setBy types.JID, updateInfo bool) bool {
	if portal.IsNewsletter() {
		return false
	}
	portal.avatarLock.Lock()
	defer portal.avatarLock.Unlock()
	changed := user.updateAvatar(ctx, portal.Key.JID, portal.IsParent, &portal.Avatar, &portal.AvatarURL, &portal.AvatarSet, portal.MainIntent())
	return portal.setRoomAvatar(ctx, changed, setBy, updateInfo)
}

func (portal *Portal) setRoomAvatar(ctx context.Context, changed bool, setBy types.JID, updateInfo bool) bool {
	log := zerolog.Ctx(ctx)
	if !changed || portal.Avatar == "unauthorized" {
		if changed || updateInfo {
			err := portal.Update(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to save portal in setRoomAvatar")
			}
		}
		return changed
	}

	if len(portal.MXID) > 0 {
		intent := portal.MainIntent()
		if !setBy.IsEmpty() && setBy.Server == types.DefaultUserServer {
			intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
		}
		_, err := intent.SetRoomAvatar(ctx, portal.MXID, portal.AvatarURL)
		if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
			_, err = portal.MainIntent().SetRoomAvatar(ctx, portal.MXID, portal.AvatarURL)
		}
		if err != nil {
			log.Err(err).Msg("Failed to set room avatar")
			return true
		} else {
			portal.AvatarSet = true
		}
	}
	if updateInfo {
		portal.UpdateBridgeInfo(ctx)
		err := portal.Update(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save portal in setRoomAvatar")
		}
		portal.updateChildRooms(ctx)
	}
	return true
}

func (portal *Portal) UpdateName(ctx context.Context, name string, setBy types.JID, updateInfo bool) bool {
	if name == "" && portal.IsBroadcastList() {
		name = UnnamedBroadcastName
	}
	if portal.Name == name && (portal.NameSet || len(portal.MXID) == 0 || !portal.shouldSetDMRoomMetadata()) {
		return false
	}
	log := zerolog.Ctx(ctx)
	log.Debug().Str("old_name", portal.Name).Str("new_name", name).Msg("Updating room name")
	portal.Name = name
	portal.NameSet = false
	if updateInfo {
		defer func() {
			err := portal.Update(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to save portal after updating name")
			}
		}()
	}
	if len(portal.MXID) == 0 {
		return true
	}
	if !portal.shouldSetDMRoomMetadata() {
		// TODO only do this if updateInfo?
		portal.UpdateBridgeInfo(ctx)
		return true
	}
	intent := portal.MainIntent()
	if !setBy.IsEmpty() && setBy.Server == types.DefaultUserServer {
		intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
	}
	_, err := intent.SetRoomName(ctx, portal.MXID, name)
	if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
		_, err = portal.MainIntent().SetRoomName(ctx, portal.MXID, name)
	}
	if err != nil {
		log.Err(err).Msg("Failed to set room name")
	} else {
		portal.NameSet = true
		if updateInfo {
			portal.UpdateBridgeInfo(ctx)
			portal.updateChildRooms(ctx)
		}
	}
	return true
}

func (portal *Portal) UpdateTopic(ctx context.Context, topic string, setBy types.JID, updateInfo bool) bool {
	if portal.Topic == topic && (portal.TopicSet || len(portal.MXID) == 0) {
		return false
	}
	log := zerolog.Ctx(ctx)
	log.Debug().Str("old_topic", portal.Topic).Str("new_topic", topic).Msg("Updating topic")
	portal.Topic = topic
	portal.TopicSet = false
	if updateInfo {
		defer func() {
			err := portal.Update(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to save portal after updating topic")
			}
		}()
	}

	intent := portal.MainIntent()
	if !setBy.IsEmpty() && setBy.Server == types.DefaultUserServer {
		intent = portal.bridge.GetPuppetByJID(setBy).IntentFor(portal)
	}
	_, err := intent.SetRoomTopic(ctx, portal.MXID, topic)
	if errors.Is(err, mautrix.MForbidden) && intent != portal.MainIntent() {
		_, err = portal.MainIntent().SetRoomTopic(ctx, portal.MXID, topic)
	}
	if err != nil {
		log.Err(err).Msg("Failed to set room topic")
	} else {
		portal.TopicSet = true
		if updateInfo {
			portal.UpdateBridgeInfo(ctx)
		}
	}
	return true
}

func newsletterToGroupInfo(meta *types.NewsletterMetadata) *types.GroupInfo {
	var out types.GroupInfo
	out.JID = meta.ID
	out.Name = meta.ThreadMeta.Name.Text
	out.NameSetAt = meta.ThreadMeta.Name.UpdateTime.Time
	out.Topic = meta.ThreadMeta.Description.Text
	out.TopicSetAt = meta.ThreadMeta.Description.UpdateTime.Time
	out.TopicID = meta.ThreadMeta.Description.ID
	out.GroupCreated = meta.ThreadMeta.CreationTime.Time
	out.IsAnnounce = true
	out.IsLocked = true
	out.IsIncognito = true
	return &out
}

func (portal *Portal) UpdateParentGroup(ctx context.Context, source *User, parent types.JID, updateInfo bool) bool {
	portal.parentGroupUpdateLock.Lock()
	defer portal.parentGroupUpdateLock.Unlock()
	if portal.ParentGroup != parent {
		zerolog.Ctx(ctx).Debug().
			Stringer("old_parent_group", portal.ParentGroup).
			Stringer("new_parent_group", parent).
			Msg("Updating parent group")
		portal.updateCommunitySpace(ctx, source, false, false)
		portal.ParentGroup = parent
		portal.parentPortal = nil
		portal.InSpace = false
		portal.updateCommunitySpace(ctx, source, true, false)
		if updateInfo {
			portal.UpdateBridgeInfo(ctx)
			err := portal.Update(ctx)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).Msg("Failed to save portal after updating parent group")
			}
		}
		return true
	} else if !portal.ParentGroup.IsEmpty() && !portal.InSpace {
		return portal.updateCommunitySpace(ctx, source, true, updateInfo)
	}
	return false
}

func (portal *Portal) UpdateMetadata(ctx context.Context, user *User, groupInfo *types.GroupInfo, newsletterMetadata *types.NewsletterMetadata) bool {
	if portal.IsPrivateChat() {
		return false
	} else if portal.IsStatusBroadcastList() {
		update := false
		update = portal.UpdateName(ctx, StatusBroadcastName, types.EmptyJID, false) || update
		update = portal.UpdateTopic(ctx, StatusBroadcastTopic, types.EmptyJID, false) || update
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
	if groupInfo == nil && portal.IsNewsletter() {
		if newsletterMetadata == nil {
			var err error
			newsletterMetadata, err = user.Client.GetNewsletterInfo(portal.Key.JID)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).Msg("Failed to get newsletter info")
				return false
			}
		}
		groupInfo = newsletterToGroupInfo(newsletterMetadata)
	}
	if groupInfo == nil {
		var err error
		groupInfo, err = user.Client.GetGroupInfo(portal.Key.JID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get group info")
			return false
		}
	}

	portal.SyncParticipants(ctx, user, groupInfo)
	update := false
	update = portal.UpdateName(ctx, groupInfo.Name, groupInfo.NameSetBy, false) || update
	update = portal.UpdateTopic(ctx, groupInfo.Topic, groupInfo.TopicSetBy, false) || update
	update = portal.UpdateParentGroup(ctx, user, groupInfo.LinkedParentJID, false) || update
	if portal.ExpirationTime != groupInfo.DisappearingTimer {
		update = true
		portal.ExpirationTime = groupInfo.DisappearingTimer
	}
	if portal.IsParent != groupInfo.IsParent {
		if portal.MXID != "" {
			zerolog.Ctx(ctx).Warn().Bool("new_is_parent", groupInfo.IsParent).Msg("Existing group changed is_parent status")
		}
		portal.IsParent = groupInfo.IsParent
		update = true
	}

	portal.RestrictMessageSending(ctx, groupInfo.IsAnnounce)
	portal.RestrictMetadataChanges(ctx, groupInfo.IsLocked)
	if newsletterMetadata != nil && newsletterMetadata.ViewerMeta != nil {
		portal.PromoteNewsletterUser(ctx, user, newsletterMetadata.ViewerMeta.Role)
	}

	return update
}

func (portal *Portal) ensureUserInvited(ctx context.Context, user *User) bool {
	return user.ensureInvited(ctx, portal.MainIntent(), portal.MXID, portal.IsPrivateChat())
}

func (portal *Portal) UpdateMatrixRoom(ctx context.Context, user *User, groupInfo *types.GroupInfo, newsletterMetadata *types.NewsletterMetadata) bool {
	if len(portal.MXID) == 0 {
		return false
	}
	log := zerolog.Ctx(ctx).With().
		Str("action", "update matrix room").
		Str("portal_key", portal.Key.String()).
		Stringer("source_mxid", user.MXID).
		Logger()
	ctx = log.WithContext(ctx)
	log.Info().Msg("Syncing portal")

	portal.ensureUserInvited(ctx, user)
	go portal.addToPersonalSpace(ctx, user)

	if groupInfo == nil && newsletterMetadata != nil {
		groupInfo = newsletterToGroupInfo(newsletterMetadata)
	}

	update := false
	update = portal.UpdateMetadata(ctx, user, groupInfo, newsletterMetadata) || update
	if !portal.IsPrivateChat() && !portal.IsBroadcastList() && !portal.IsNewsletter() {
		update = portal.UpdateAvatar(ctx, user, types.EmptyJID, false) || update
	} else if newsletterMetadata != nil {
		update = portal.UpdateNewsletterAvatar(ctx, user, newsletterMetadata) || update
	}
	if update || portal.LastSync.Add(24*time.Hour).Before(time.Now()) {
		portal.LastSync = time.Now()
		err := portal.Update(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save portal after updating")
		}
		portal.UpdateBridgeInfo(ctx)
		portal.updateChildRooms(ctx)
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

func (portal *Portal) ChangeAdminStatus(ctx context.Context, jids []types.JID, setAdmin bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(ctx, portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if setAdmin {
		newLevel = 50
	}
	changed := portal.applyPowerLevelFixes(levels)
	for _, jid := range jids {
		if jid.Server != types.DefaultUserServer {
			// TODO handle lids
			continue
		}
		puppet := portal.bridge.GetPuppetByJID(jid)
		changed = levels.EnsureUserLevel(puppet.MXID, newLevel) || changed

		user := portal.bridge.GetUserByJID(jid)
		if user != nil {
			changed = levels.EnsureUserLevel(user.MXID, newLevel) || changed
		}
	}
	if changed {
		resp, err := portal.MainIntent().SetPowerLevels(ctx, portal.MXID, levels)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to set power levels")
		} else {
			return resp.EventID
		}
	}
	return ""
}

func (portal *Portal) RestrictMessageSending(ctx context.Context, restrict bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(ctx, portal.MXID)
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
	resp, err := portal.MainIntent().SetPowerLevels(ctx, portal.MXID, levels)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to set power levels")
		return ""
	} else {
		return resp.EventID
	}
}

func (portal *Portal) PromoteNewsletterUser(ctx context.Context, user *User, role types.NewsletterRole) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(ctx, portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}

	newLevel := 0
	switch role {
	case types.NewsletterRoleAdmin:
		newLevel = 50
	case types.NewsletterRoleOwner:
		newLevel = 95
	}

	changed := portal.applyPowerLevelFixes(levels)
	changed = levels.EnsureUserLevel(user.MXID, newLevel) || changed
	if !changed {
		return ""
	}

	resp, err := portal.MainIntent().SetPowerLevels(ctx, portal.MXID, levels)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to set power levels")
		return ""
	} else {
		return resp.EventID
	}
}

func (portal *Portal) RestrictMetadataChanges(ctx context.Context, restrict bool) id.EventID {
	levels, err := portal.MainIntent().PowerLevels(ctx, portal.MXID)
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
		resp, err := portal.MainIntent().SetPowerLevels(ctx, portal.MXID, levels)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to set power levels")
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

func (portal *Portal) UpdateBridgeInfo(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	if len(portal.MXID) == 0 {
		log.Debug().Msg("Not updating bridge info: no Matrix room created")
		return
	}
	log.Debug().Msg("Updating bridge info...")
	stateKey, content := portal.getBridgeInfo()
	_, err := portal.MainIntent().SendStateEvent(ctx, portal.MXID, event.StateBridge, stateKey, content)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to update m.bridge info")
	}
	// TODO remove this once https://github.com/matrix-org/matrix-doc/pull/2346 is in spec
	_, err = portal.MainIntent().SendStateEvent(ctx, portal.MXID, event.StateHalfShotBridge, stateKey, content)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to update uk.half-shot.bridge info")
	}
}

func (portal *Portal) updateChildRooms(ctx context.Context) {
	if !portal.IsParent {
		return
	}
	children := portal.bridge.GetAllByParentGroup(portal.Key.JID)
	for _, child := range children {
		changed := child.updateCommunitySpace(ctx, nil, true, false)
		// TODO set updateInfo to true instead of updating manually?
		child.UpdateBridgeInfo(ctx)
		if changed {
			// TODO is this saving the wrong portal?
			err := portal.Update(ctx)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).Msg("Failed to save portal after updating")
			}
		}
	}
}

func (portal *Portal) shouldSetDMRoomMetadata() bool {
	return !portal.IsPrivateChat() ||
		portal.bridge.Config.Bridge.PrivateChatPortalMeta == "always" ||
		(portal.IsEncrypted() && portal.bridge.Config.Bridge.PrivateChatPortalMeta != "never")
}

func (portal *Portal) GetEncryptionEventContent() (evt *event.EncryptionEventContent) {
	evt = &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}
	if rot := portal.bridge.Config.Bridge.Encryption.Rotation; rot.EnableCustom {
		evt.RotationPeriodMillis = rot.Milliseconds
		evt.RotationPeriodMessages = rot.Messages
	}
	return
}

func (portal *Portal) CreateMatrixRoom(ctx context.Context, user *User, groupInfo *types.GroupInfo, newsletterMetadata *types.NewsletterMetadata, isFullInfo, backfill bool) error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if len(portal.MXID) > 0 {
		return nil
	}
	log := zerolog.Ctx(ctx).With().
		Str("action", "create matrix room").
		Str("portal_key", portal.Key.String()).
		Stringer("source_mxid", user.MXID).
		Logger()
	ctx = log.WithContext(ctx)

	intent := portal.MainIntent()
	if err := intent.EnsureRegistered(ctx); err != nil {
		return err
	}

	log.Info().Msg("Creating Matrix room")

	//var broadcastMetadata *types.BroadcastListInfo
	if portal.IsPrivateChat() {
		puppet := portal.bridge.GetPuppetByJID(portal.Key.JID)
		puppet.SyncContact(ctx, user, true, false, "creating private chat portal")
		portal.Name = puppet.Displayname
		portal.AvatarURL = puppet.AvatarURL
		portal.Avatar = puppet.Avatar
		portal.Topic = PrivateChatTopic
	} else if portal.IsStatusBroadcastList() {
		if !portal.bridge.Config.Bridge.EnableStatusBroadcast {
			log.Debug().Msg("Status bridging is disabled in config, not creating room after all")
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
		log.Debug().Msg("Broadcast list is not yet supported, not creating room after all")
		return fmt.Errorf("broadcast list bridging is currently not supported")
	} else {
		if portal.IsNewsletter() {
			if newsletterMetadata == nil {
				var err error
				newsletterMetadata, err = user.Client.GetNewsletterInfo(portal.Key.JID)
				if err != nil {
					return err
				}
			}
			if groupInfo == nil {
				groupInfo = newsletterToGroupInfo(newsletterMetadata)
			}
		} else if groupInfo == nil || !isFullInfo {
			foundInfo, err := user.Client.GetGroupInfo(portal.Key.JID)

			// Ensure that the user is actually a participant in the conversation
			// before creating the matrix room
			if errors.Is(err, whatsmeow.ErrNotInGroup) {
				log.Debug().Msg("Skipping creating room because the user is not a participant")
				err = user.bridge.DB.BackfillQueue.DeleteAllForPortal(ctx, user.MXID, portal.Key)
				if err != nil {
					log.Err(err).Msg("Failed to delete backfill queue for portal")
				}
				err = user.bridge.DB.HistorySync.DeleteAllMessagesForPortal(ctx, user.MXID, portal.Key)
				if err != nil {
					log.Err(err).Msg("Failed to delete historical messages for portal")
				}
				return err
			} else if err != nil {
				log.Err(err).Msg("Failed to get group info")
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
		if portal.IsNewsletter() {
			portal.UpdateNewsletterAvatar(ctx, user, newsletterMetadata)
		} else {
			portal.UpdateAvatar(ctx, user, types.EmptyJID, false)
		}
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
	if newsletterMetadata != nil && newsletterMetadata.ViewerMeta != nil {
		switch newsletterMetadata.ViewerMeta.Role {
		case types.NewsletterRoleAdmin:
			powerLevels.EnsureUserLevel(user.MXID, 50)
		case types.NewsletterRoleOwner:
			powerLevels.EnsureUserLevel(user.MXID, 95)
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
	if !portal.AvatarURL.IsEmpty() && portal.shouldSetDMRoomMetadata() {
		initialState = append(initialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{
				Parsed: event.RoomAvatarEventContent{URL: portal.AvatarURL},
			},
		})
		portal.AvatarSet = true
	} else {
		portal.AvatarSet = false
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
	autoJoinInvites := portal.bridge.SpecVersions.Supports(mautrix.BeeperFeatureAutojoinInvites)
	if autoJoinInvites {
		log.Debug().Msg("Hungryserv mode: adding all group members in create request")
		if groupInfo != nil && !portal.IsNewsletter() {
			// TODO non-hungryserv could also include all members in invites, and then send joins manually?
			participants, powerLevels := portal.SyncParticipants(ctx, user, groupInfo)
			invite = append(invite, participants...)
			if initialState[0].Type != event.StatePowerLevels {
				panic(fmt.Errorf("unexpected type %s in first initial state event", initialState[0].Type.Type))
			}
			initialState[0].Content.Parsed = powerLevels
		} else {
			invite = append(invite, user.MXID)
		}
	}
	req := &mautrix.ReqCreateRoom{
		Visibility:      "private",
		Name:            portal.Name,
		Topic:           portal.Topic,
		Invite:          invite,
		Preset:          "private_chat",
		IsDirect:        portal.IsPrivateChat(),
		InitialState:    initialState,
		CreationContent: creationContent,

		BeeperAutoJoinInvites: autoJoinInvites,
	}
	if !portal.shouldSetDMRoomMetadata() {
		req.Name = ""
	}
	legacyBackfill := user.bridge.Config.Bridge.HistorySync.Backfill && backfill && !user.bridge.SpecVersions.Supports(mautrix.BeeperFeatureBatchSending)
	var backfillStarted bool
	if legacyBackfill {
		portal.latestEventBackfillLock.Lock()
		defer func() {
			if !backfillStarted {
				portal.latestEventBackfillLock.Unlock()
			}
		}()
	}
	resp, err := intent.CreateRoom(ctx, req)
	if err != nil {
		return err
	}
	log.Info().Stringer("room_id", resp.RoomID).Msg("Matrix room created")
	portal.InSpace = false
	portal.NameSet = len(req.Name) > 0
	portal.TopicSet = len(req.Topic) > 0
	portal.MXID = resp.RoomID
	portal.updateLogger()
	portal.bridge.portalsLock.Lock()
	portal.bridge.portalsByMXID[portal.MXID] = portal
	portal.bridge.portalsLock.Unlock()
	err = portal.Update(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to save portal after creating room")
	}

	// We set the memberships beforehand to make sure the encryption key exchange in initial backfill knows the users are here.
	inviteMembership := event.MembershipInvite
	if autoJoinInvites {
		inviteMembership = event.MembershipJoin
	}
	for _, userID := range invite {
		err = portal.bridge.StateStore.SetMembership(ctx, portal.MXID, userID, inviteMembership)
		if err != nil {
			log.Err(err).Stringer("user_id", userID).Msg("Failed to update membership in state store")
		}
	}

	if !autoJoinInvites {
		portal.ensureUserInvited(ctx, user)
	}
	user.syncChatDoublePuppetDetails(ctx, portal, true)

	go portal.updateCommunitySpace(ctx, user, true, true)
	go portal.addToPersonalSpace(ctx, user)

	if !portal.IsNewsletter() && groupInfo != nil && !autoJoinInvites {
		portal.SyncParticipants(ctx, user, groupInfo)
	}
	//if broadcastMetadata != nil {
	//	portal.SyncBroadcastRecipients(user, broadcastMetadata)
	//}
	if portal.IsPrivateChat() {
		puppet := user.bridge.GetPuppetByJID(portal.Key.JID)

		if portal.bridge.Config.Bridge.Encryption.Default {
			err = portal.bridge.Bot.EnsureJoined(ctx, portal.MXID)
			if err != nil {
				log.Err(err).Msg("Failed to ensure bridge bot is joined to created portal")
			}
		}

		user.UpdateDirectChats(ctx, map[id.UserID][]id.RoomID{puppet.MXID: {portal.MXID}})
	} else if portal.IsParent {
		portal.updateChildRooms(ctx)
	}

	if user.bridge.Config.Bridge.HistorySync.Backfill && backfill {
		if legacyBackfill {
			backfillStarted = true
			go portal.legacyBackfill(context.WithoutCancel(ctx), user)
		} else {
			portals := []*Portal{portal}
			user.EnqueueImmediateBackfills(ctx, portals)
			user.EnqueueDeferredBackfills(ctx, portals)
			user.BackfillQueue.ReCheck()
		}
	}
	return nil
}

func (portal *Portal) addToPersonalSpace(ctx context.Context, user *User) {
	spaceID := user.GetSpaceRoom(ctx)
	if len(spaceID) == 0 || user.IsInSpace(ctx, portal.Key) {
		return
	}
	_, err := portal.bridge.Bot.SendStateEvent(ctx, spaceID, event.StateSpaceChild, portal.MXID.String(), &event.SpaceChildEventContent{
		Via: []string{portal.bridge.Config.Homeserver.Domain},
	})
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Stringer("space_id", spaceID).Msg("Failed to add portal to user's personal filtering space")
	} else {
		zerolog.Ctx(ctx).Debug().Stringer("space_id", spaceID).Msg("Added portal to user's personal filtering space")
		user.MarkInSpace(ctx, portal.Key)
	}
}

func (portal *Portal) removeSpaceParentEvent(space id.RoomID) {
	_, err := portal.MainIntent().SendStateEvent(context.TODO(), portal.MXID, event.StateSpaceParent, space.String(), &event.SpaceParentEventContent{})
	if err != nil {
		portal.zlog.Err(err).Stringer("space_mxid", space).Msg("Failed to send m.space.parent event to remove portal from space")
	}
}

func (portal *Portal) updateCommunitySpace(ctx context.Context, user *User, add, updateInfo bool) bool {
	if add == portal.InSpace {
		return false
	}
	// TODO if this function is changed to use the context logger, updateChildRooms should add the child portal info to the logger
	log := portal.zlog.With().Stringer("room_id", portal.MXID).Logger()
	space := portal.GetParentPortal()
	if space == nil {
		return false
	} else if space.MXID == "" {
		if !add || user == nil {
			return false
		}
		log.Debug().Stringer("parent_group_jid", space.Key.JID).Msg("Creating portal for parent group")
		err := space.CreateMatrixRoom(ctx, user, nil, nil, false, false)
		if err != nil {
			log.Err(err).Msg("Failed to create portal for parent group")
			return false
		}
	}

	var parentContent event.SpaceParentEventContent
	var childContent event.SpaceChildEventContent
	if add {
		parentContent.Canonical = true
		parentContent.Via = []string{portal.bridge.Config.Homeserver.Domain}
		childContent.Via = []string{portal.bridge.Config.Homeserver.Domain}
		log.Debug().
			Stringer("space_mxid", space.MXID).
			Stringer("parent_group_jid", space.Key.JID).
			Msg("Adding room to parent group space")
	} else {
		log.Debug().
			Stringer("space_mxid", space.MXID).
			Stringer("parent_group_jid", space.Key.JID).
			Msg("Removing room from parent group space")
	}

	_, err := space.MainIntent().SendStateEvent(ctx, space.MXID, event.StateSpaceChild, portal.MXID.String(), &childContent)
	if err != nil {
		log.Err(err).Stringer("space_mxid", space.MXID).Msg("Failed to send m.space.child event")
		return false
	}
	_, err = portal.MainIntent().SendStateEvent(ctx, portal.MXID, event.StateSpaceParent, space.MXID.String(), &parentContent)
	if err != nil {
		log.Err(err).Stringer("space_mxid", space.MXID).Msg("Failed to send m.space.parent event")
	}
	portal.InSpace = add
	if updateInfo {
		err = portal.Update(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save portal after updating parent space")
		}
		portal.UpdateBridgeInfo(ctx)
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

func (portal *Portal) IsNewsletter() bool {
	return portal.Key.JID.Server == types.NewsletterServer
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

func (portal *Portal) addReplyMention(content *event.MessageEventContent, sender types.JID, senderMXID id.UserID) {
	if content.Mentions == nil || (sender.IsEmpty() && senderMXID == "") {
		return
	}
	// TODO handle lids
	if senderMXID == "" && sender.Server == types.DefaultUserServer {
		if user := portal.bridge.GetUserByJID(sender); user != nil {
			senderMXID = user.MXID
		} else {
			puppet := portal.bridge.GetPuppetByJID(sender)
			senderMXID = puppet.MXID
		}
	}
	if senderMXID != "" && !slices.Contains(content.Mentions.UserIDs, senderMXID) {
		content.Mentions.UserIDs = append(content.Mentions.UserIDs, senderMXID)
	}
}

func (portal *Portal) SetReply(ctx context.Context, content *event.MessageEventContent, replyTo *ReplyInfo, isHungryBackfill bool) bool {
	if replyTo == nil {
		return false
	}
	log := zerolog.Ctx(ctx).With().
		Object("reply_to", replyTo).
		Str("action", "SetReply").
		Logger()
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
		} else if replyTo.Chat == types.StatusBroadcastJID {
			key = database.NewPortalKey(replyTo.Chat, key.Receiver)
		}
		if key != portal.Key {
			targetPortal = portal.bridge.GetExistingPortalByJID(key)
			if targetPortal == nil {
				return false
			}
		}
	}
	message, err := portal.bridge.DB.Message.GetByJID(ctx, key, replyTo.MessageID)
	if err != nil {
		log.Err(err).Msg("Failed to get reply target from database")
		return false
	} else if message == nil || message.IsFakeMXID() {
		if isHungryBackfill {
			content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(targetPortal.deterministicEventID(replyTo.Sender, replyTo.MessageID, ""))
			portal.addReplyMention(content, replyTo.Sender, "")
			return true
		} else {
			log.Warn().Msg("Failed to find reply target")
		}
		return false
	}
	portal.addReplyMention(content, message.Sender, message.SenderMXID)
	content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(message.MXID)
	if portal.bridge.Config.Bridge.DisableReplyFallbacks {
		return true
	}
	evt, err := targetPortal.MainIntent().GetEvent(ctx, targetPortal.MXID, message.MXID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get reply target event")
		return true
	}
	_ = evt.Content.ParseRaw(evt.Type)
	if evt.Type == event.EventEncrypted {
		decryptedEvt, err := portal.bridge.Crypto.Decrypt(ctx, evt)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to decrypt reply target event")
		} else {
			evt = decryptedEvt
		}
	}
	content.SetReply(evt)
	return true
}

func (portal *Portal) HandleMessageReaction(ctx context.Context, intent *appservice.IntentAPI, user *User, info *types.MessageInfo, reaction *waProto.ReactionMessage, existingMsg *database.Message) {
	if existingMsg != nil {
		_, _ = portal.MainIntent().RedactEvent(ctx, portal.MXID, existingMsg.MXID, mautrix.ReqRedact{
			Reason: "The undecryptable message was actually a reaction",
		})
	}

	targetJID := reaction.GetKey().GetId()
	log := zerolog.Ctx(ctx).With().
		Str("reaction_target_id", targetJID).
		Logger()
	if reaction.GetText() == "" {
		existing, err := portal.bridge.DB.Reaction.GetByTargetJID(ctx, portal.Key, targetJID, info.Sender)
		if err != nil {
			log.Err(err).Msg("Failed to get existing reaction to remove")
			return
		} else if existing == nil {
			log.Debug().Msg("Dropping removal of unknown reaction")
			return
		}

		resp, err := intent.RedactEvent(ctx, portal.MXID, existing.MXID)
		if err != nil {
			log.Err(err).
				Stringer("reaction_mxid", existing.MXID).
				Msg("Failed to redact reaction")
		}
		portal.finishHandling(ctx, existingMsg, info, resp.EventID, intent.UserID, database.MsgReaction, 0, database.MsgNoError)
		err = existing.Delete(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to delete reaction from database")
		}
	} else {
		target, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, targetJID)
		if err != nil {
			log.Err(err).Msg("Failed to get reaction target message from database")
			return
		} else if target == nil {
			log.Debug().Msg("Dropping reaction to unknown message")
			return
		}

		var content event.ReactionEventContent
		content.RelatesTo = event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: target.MXID,
			Key:     variationselector.Add(reaction.GetText()),
		}
		resp, err := intent.SendMassagedMessageEvent(ctx, portal.MXID, event.EventReaction, &content, info.Timestamp.UnixMilli())
		if err != nil {
			log.Err(err).Msg("Failed to bridge reaction")
			return
		}

		portal.finishHandling(ctx, existingMsg, info, resp.EventID, intent.UserID, database.MsgReaction, 0, database.MsgNoError)
		portal.upsertReaction(ctx, intent, target.JID, info.Sender, resp.EventID, info.ID)
	}
}

func (portal *Portal) HandleMessageRevoke(ctx context.Context, user *User, info *types.MessageInfo, key *waProto.MessageKey) bool {
	log := zerolog.Ctx(ctx).With().Str("revoke_target_id", key.GetId()).Logger()
	msg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, key.GetId())
	if err != nil {
		log.Err(err).Msg("Failed to get revoke target message from database")
		return false
	} else if msg == nil || msg.IsFakeMXID() {
		return false
	}
	intent := portal.bridge.GetPuppetByJID(info.Sender).IntentFor(portal)
	_, err = intent.RedactEvent(ctx, portal.MXID, msg.MXID)
	if errors.Is(err, mautrix.MForbidden) {
		_, err = portal.MainIntent().RedactEvent(ctx, portal.MXID, msg.MXID)
	}
	if err != nil {
		log.Err(err).Stringer("revoke_target_mxid", msg.MXID).Msg("Failed to redact message from revoke")
	} else if err = msg.Delete(ctx); err != nil {
		log.Err(err).Msg("Failed to delete message from database after revoke")
	}
	return true
}

func (portal *Portal) deleteForMe(ctx context.Context, user *User, content *events.DeleteForMe) bool {
	matrixUsers, err := portal.GetMatrixUsers(ctx)
	if err != nil {
		portal.zlog.Err(err).Msg("Failed to get Matrix users in portal to see if DeleteForMe should be handled")
		return false
	}
	if len(matrixUsers) == 1 && matrixUsers[0] == user.MXID {
		msg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, content.MessageID)
		if msg == nil || msg.IsFakeMXID() {
			return false
		}
		_, err = portal.MainIntent().RedactEvent(ctx, portal.MXID, msg.MXID)
		if err != nil {
			portal.zlog.Err(err).Str("message_id", msg.JID).Msg("Failed to redact message from DeleteForMe")
		} else if err = msg.Delete(ctx); err != nil {
			portal.zlog.Err(err).Str("message_id", msg.JID).Msg("Failed to delete message from database after DeleteForMe")
		}
		return true
	}
	return false
}

func (portal *Portal) sendMainIntentMessage(ctx context.Context, content *event.MessageEventContent) (*mautrix.RespSendEvent, error) {
	return portal.sendMessage(ctx, portal.MainIntent(), event.EventMessage, content, nil, 0)
}

func (portal *Portal) encrypt(ctx context.Context, intent *appservice.IntentAPI, content *event.Content, eventType event.Type) (event.Type, error) {
	if !portal.Encrypted || portal.bridge.Crypto == nil {
		return eventType, nil
	}
	intent.AddDoublePuppetValue(content)
	// TODO maybe the locking should be inside mautrix-go?
	portal.encryptLock.Lock()
	defer portal.encryptLock.Unlock()
	err := portal.bridge.Crypto.Encrypt(ctx, portal.MXID, eventType, content)
	if err != nil {
		return eventType, fmt.Errorf("failed to encrypt event: %w", err)
	}
	return event.EventEncrypted, nil
}

func (portal *Portal) sendMessage(ctx context.Context, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, timestamp int64) (*mautrix.RespSendEvent, error) {
	wrappedContent := event.Content{Parsed: content, Raw: extraContent}
	var err error
	eventType, err = portal.encrypt(ctx, intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	_, _ = intent.UserTyping(ctx, portal.MXID, false, 0)
	if timestamp == 0 {
		return intent.SendMessageEvent(ctx, portal.MXID, eventType, &wrappedContent)
	} else {
		return intent.SendMassagedMessageEvent(ctx, portal.MXID, eventType, &wrappedContent, timestamp)
	}
}

type ReplyInfo struct {
	MessageID types.MessageID
	Chat      types.JID
	Sender    types.JID
}

func (r *ReplyInfo) Equals(other *ReplyInfo) bool {
	if r == nil {
		return other == nil
	} else if other == nil {
		return false
	}
	return r.MessageID == other.MessageID && r.Chat == other.Chat && r.Sender == other.Sender
}

func (r ReplyInfo) MarshalZerologObject(e *zerolog.Event) {
	e.Str("message_id", r.MessageID)
	e.Str("chat_jid", r.Chat.String())
	e.Str("sender_jid", r.Sender.String())
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
func (portal *Portal) convertTextMessage(ctx context.Context, intent *appservice.IntentAPI, source *User, msg *waProto.Message) *ConvertedMessage {
	content := &event.MessageEventContent{
		Body:    msg.GetConversation(),
		MsgType: event.MsgText,
	}
	if len(msg.GetExtendedTextMessage().GetText()) > 0 {
		content.Body = msg.GetExtendedTextMessage().GetText()
	}

	contextInfo := msg.GetExtendedTextMessage().GetContextInfo()
	portal.bridge.Formatter.ParseWhatsApp(ctx, portal.MXID, content, contextInfo.GetMentionedJid(), false, false)
	expiresIn := time.Duration(contextInfo.GetExpiration()) * time.Second
	extraAttrs := map[string]interface{}{}
	extraAttrs["com.beeper.linkpreviews"] = portal.convertURLPreviewToBeeper(ctx, intent, source, msg.GetExtendedTextMessage())

	return &ConvertedMessage{
		Intent:    intent,
		Type:      event.EventMessage,
		Content:   content,
		ReplyTo:   GetReply(contextInfo),
		ExpiresIn: expiresIn,
		Extra:     extraAttrs,
	}
}

func (portal *Portal) convertTemplateMessage(ctx context.Context, intent *appservice.IntentAPI, source *User, info *types.MessageInfo, tplMsg *waProto.TemplateMessage) *ConvertedMessage {
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
		convertedTitle = portal.convertMediaMessage(ctx, intent, source, info, title.DocumentMessage, "file attachment", false)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_ImageMessage:
		convertedTitle = portal.convertMediaMessage(ctx, intent, source, info, title.ImageMessage, "photo", false)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_VideoMessage:
		convertedTitle = portal.convertMediaMessage(ctx, intent, source, info, title.VideoMessage, "video attachment", false)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_LocationMessage:
		content = fmt.Sprintf("Unsupported location message\n\n%s", content)
	case *waProto.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText:
		content = fmt.Sprintf("%s\n\n%s", title.HydratedTitleText, content)
	}

	converted.Content.Body = content
	portal.bridge.Formatter.ParseWhatsApp(ctx, portal.MXID, converted.Content, nil, true, false)
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

func (portal *Portal) convertTemplateButtonReplyMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.TemplateButtonReplyMessage) *ConvertedMessage {
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

func (portal *Portal) convertListMessage(ctx context.Context, intent *appservice.IntentAPI, source *User, msg *waProto.ListMessage) *ConvertedMessage {
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
	randomID := random.String(64)
	body = fmt.Sprintf("%s\n%s", body, randomID)
	if msg.GetFooterText() != "" {
		body = fmt.Sprintf("%s\n\n%s", body, msg.GetFooterText())
	}
	converted.Content.Body = body
	portal.bridge.Formatter.ParseWhatsApp(ctx, portal.MXID, converted.Content, nil, false, true)

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

func (portal *Portal) convertListResponseMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.ListResponseMessage) *ConvertedMessage {
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

func (portal *Portal) convertPollUpdateMessage(ctx context.Context, intent *appservice.IntentAPI, source *User, info *types.MessageInfo, msg *waProto.PollUpdateMessage) *ConvertedMessage {
	log := zerolog.Ctx(ctx).With().
		Str("poll_id", msg.GetPollCreationMessageKey().GetId()).
		Logger()
	pollMessage, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, msg.GetPollCreationMessageKey().GetId())
	if err != nil {
		log.Err(err).Msg("Failed to get poll message to convert vote")
		return nil
	} else if pollMessage == nil {
		log.Warn().Msg("Poll message not found for converting vote message")
		return nil
	}
	vote, err := source.Client.DecryptPollVote(&events.Message{
		Info:    *info,
		Message: &waProto.Message{PollUpdateMessage: msg},
	})
	if err != nil {
		log.Err(err).Msg("Failed to decrypt vote message")
		return nil
	}
	selectedHashes := make([]string, len(vote.GetSelectedOptions()))
	if pollMessage.Type == database.MsgMatrixPoll {
		mappedAnswers, err := pollMessage.GetPollOptionIDs(ctx, vote.GetSelectedOptions())
		if err != nil {
			log.Err(err).Msg("Failed to get poll option IDs")
			return nil
		}
		for i, opt := range vote.GetSelectedOptions() {
			if len(opt) != 32 {
				log.Warn().Int("hash_len", len(opt)).Msg("Unexpected option hash length in vote")
				continue
			}
			var ok bool
			selectedHashes[i], ok = mappedAnswers[[32]byte(opt)]
			if !ok {
				log.Warn().Hex("option_hash", opt).Msg("Didn't find ID for option in vote")
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
			//"org.matrix.msc3381.v2.selections": selectedHashes,
		},
	}
}

func (portal *Portal) convertPollCreationMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.PollCreationMessage) *ConvertedMessage {
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

			// Slightly less extensible events (November 2022)
			//"org.matrix.msc1767.markup": []map[string]any{
			//	{"mimetype": "text/html", "body": formattedBody},
			//	{"mimetype": "text/plain", "body": body},
			//},
			//"org.matrix.msc3381.v2.poll": map[string]any{
			//	"kind":           "org.matrix.msc3381.v2.disclosed",
			//	"max_selections": maxChoices,
			//	"question": map[string]any{
			//		"org.matrix.msc1767.markup": []map[string]any{
			//			{"mimetype": "text/plain", "body": msg.GetName()},
			//		},
			//	},
			//	"answers": msc3381V2Answers,
			//},

			// Legacyest extensible events
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

func (portal *Portal) convertLiveLocationMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.LiveLocationMessage) *ConvertedMessage {
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

func (portal *Portal) convertLocationMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.LocationMessage) *ConvertedMessage {
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
		uploadedThumbnail, _ := intent.UploadBytes(ctx, msg.GetJpegThumbnail(), thumbnailMime)
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
const inviteMsgBroken = `%s<hr/>This invitation to join "%s" expires at %s. However, the invite message is broken or unsupported and cannot be accepted.`
const inviteMetaField = "fi.mau.whatsapp.invite"
const escapedInviteMetaField = `fi\.mau\.whatsapp\.invite`

type InviteMeta struct {
	JID        types.JID `json:"jid"`
	Code       string    `json:"code"`
	Expiration int64     `json:"expiration,string"`
	Inviter    types.JID `json:"inviter"`
}

func (portal *Portal) convertGroupInviteMessage(ctx context.Context, intent *appservice.IntentAPI, info *types.MessageInfo, msg *waProto.GroupInviteMessage) *ConvertedMessage {
	expiry := time.Unix(msg.GetInviteExpiration(), 0)
	template := inviteMsg
	var extraAttrs map[string]any
	groupJID, err := types.ParseJID(msg.GetGroupJid())
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("invite_group_jid", msg.GetGroupJid()).Msg("Failed to parse invite group JID")
		template = inviteMsgBroken
	} else {
		extraAttrs = map[string]interface{}{
			inviteMetaField: InviteMeta{
				JID:        groupJID,
				Code:       msg.GetInviteCode(),
				Expiration: msg.GetInviteExpiration(),
				Inviter:    info.Sender.ToNonAD(),
			},
		}
	}

	htmlMessage := fmt.Sprintf(template, event.TextToHTML(msg.GetCaption()), msg.GetGroupName(), expiry)
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          format.HTMLToText(htmlMessage),
		Format:        event.FormatHTML,
		FormattedBody: htmlMessage,
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

func (portal *Portal) convertContactMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.ContactMessage) *ConvertedMessage {
	fileName := fmt.Sprintf("%s.vcf", msg.GetDisplayName())
	data := []byte(msg.GetVcard())
	mimeType := "text/vcard"
	uploadMimeType, file := portal.encryptFileInPlace(data, mimeType)

	uploadResp, err := intent.UploadBytesWithName(ctx, data, uploadMimeType, fileName)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Str("displayname", msg.GetDisplayName()).Msg("Failed to upload vcard")
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

func (portal *Portal) convertContactsArrayMessage(ctx context.Context, intent *appservice.IntentAPI, msg *waProto.ContactsArrayMessage) *ConvertedMessage {
	name := msg.GetDisplayName()
	if len(name) == 0 {
		name = fmt.Sprintf("%d contacts", len(msg.GetContacts()))
	}
	contacts := make([]*event.MessageEventContent, 0, len(msg.GetContacts()))
	for _, contact := range msg.GetContacts() {
		converted := portal.convertContactMessage(ctx, intent, contact)
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

func (portal *Portal) tryKickUser(ctx context.Context, userID id.UserID, intent *appservice.IntentAPI) error {
	_, err := intent.KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{UserID: userID})
	if errors.Is(err, mautrix.MForbidden) {
		_, err = portal.MainIntent().KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{UserID: userID})
	}
	return err
}

func (portal *Portal) removeUser(ctx context.Context, isSameUser bool, kicker *appservice.IntentAPI, target id.UserID, targetIntent *appservice.IntentAPI) {
	if !isSameUser || targetIntent == nil {
		err := portal.tryKickUser(ctx, target, kicker)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Stringer("target_mxid", target).Msg("Failed to kick user from portal")
			if targetIntent != nil {
				_, _ = targetIntent.LeaveRoom(ctx, portal.MXID)
			}
		}
	} else {
		_, err := targetIntent.LeaveRoom(ctx, portal.MXID)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Stringer("target_mxid", target).Msg("Failed to leave portal as user")
			_, _ = portal.MainIntent().KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{UserID: target})
		}
	}
	portal.CleanupIfEmpty(ctx)
}

func (portal *Portal) HandleWhatsAppKick(ctx context.Context, source *User, senderJID types.JID, jids []types.JID) {
	sender := portal.bridge.GetPuppetByJID(senderJID)
	senderIntent := sender.IntentFor(portal)
	for _, jid := range jids {
		if jid.Server != types.DefaultUserServer {
			// TODO handle lids
			continue
		}
		//if source != nil && source.JID.User == jid.User {
		//	portal.log.Debugln("Ignoring self-kick by", source.MXID)
		//	continue
		//}
		puppet := portal.bridge.GetPuppetByJID(jid)
		portal.removeUser(ctx, puppet.JID == sender.JID, senderIntent, puppet.MXID, puppet.DefaultIntent())

		if !portal.IsBroadcastList() {
			user := portal.bridge.GetUserByJID(jid)
			if user != nil {
				var customIntent *appservice.IntentAPI
				if puppet.CustomMXID == user.MXID {
					customIntent = puppet.CustomIntent()
				}
				portal.removeUser(ctx, puppet.JID == sender.JID, senderIntent, user.MXID, customIntent)
			}
		}
	}
}

func (portal *Portal) HandleWhatsAppInvite(ctx context.Context, source *User, senderJID *types.JID, jids []types.JID) (evtID id.EventID) {
	intent := portal.MainIntent()
	if senderJID != nil && !senderJID.IsEmpty() {
		sender := portal.bridge.GetPuppetByJID(*senderJID)
		intent = sender.IntentFor(portal)
	}
	for _, jid := range jids {
		if jid.Server != types.DefaultUserServer {
			// TODO handle lids
			continue
		}
		puppet := portal.bridge.GetPuppetByJID(jid)
		puppet.SyncContact(ctx, source, true, false, "handling whatsapp invite")
		resp, err := intent.SendStateEvent(ctx, portal.MXID, event.StateMember, puppet.MXID.String(), &event.MemberEventContent{
			Membership:  event.MembershipInvite,
			Displayname: puppet.Displayname,
			AvatarURL:   puppet.AvatarURL.CUString(),
		})
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).
				Stringer("target_mxid", puppet.MXID).
				Stringer("inviter_mxid", intent.UserID).
				Msg("Failed to invite user")
			_ = portal.MainIntent().EnsureInvited(ctx, portal.MXID, puppet.MXID)
		} else {
			evtID = resp.EventID
		}
		err = puppet.DefaultIntent().EnsureJoined(ctx, portal.MXID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("target_mxid", puppet.MXID).
				Msg("Failed to ensure user is joined to portal")
		}
	}
	return
}

func (portal *Portal) HandleWhatsAppDeleteChat(ctx context.Context, user *User) {
	if portal.MXID == "" {
		return
	}
	matrixUsers, err := portal.GetMatrixUsers(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get Matrix users to see if DeleteChat should be handled")
		return
	}
	if len(matrixUsers) > 1 {
		zerolog.Ctx(ctx).Debug().Msg("Portal contains more than one Matrix user, ignoring DeleteChat event")
		return
	} else if (len(matrixUsers) == 1 && matrixUsers[0] == user.MXID) || len(matrixUsers) < 1 {
		zerolog.Ctx(ctx).Debug().Msg("User deleted chat and there are no other Matrix users, deleting portal...")
		portal.Delete(ctx)
		portal.Cleanup(ctx, false)
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

func (portal *Portal) makeMediaBridgeFailureMessage(info *types.MessageInfo, bridgeErr error, converted *ConvertedMessage, keys *FailedMediaKeys, userFriendlyError string) *ConvertedMessage {
	if errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith403) || errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith404) || errors.Is(bridgeErr, whatsmeow.ErrMediaDownloadFailedWith410) {
		portal.zlog.Debug().Err(bridgeErr).Str("message_id", info.ID).Msg("Failed to bridge media for message")
	} else {
		portal.zlog.Err(bridgeErr).Str("message_id", info.ID).Msg("Failed to bridge media for message")
	}
	if keys != nil {
		if portal.bridge.Config.Bridge.CaptionInMessage {
			converted.MergeCaption()
		}
		meta := &FailedMediaMeta{
			Type:         converted.Type,
			Content:      converted.Content,
			ExtraContent: maps.Clone(converted.Extra),
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

func (portal *Portal) convertMediaMessageContent(ctx context.Context, intent *appservice.IntentAPI, msg MediaMessage) *ConvertedMessage {
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

		content.Body += exmime.ExtensionFromMimetype(msg.GetMimetype())
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
		uploadedThumbnail, err := intent.UploadBytes(ctx, thumbnailData, thumbnailUploadMime)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to upload thumbnail")
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
		zerolog.Ctx(ctx).Warn().Type("content_struct", msg).Msg("Unexpected media type in convertMediaMessageContent")
		content.MsgType = event.MsgFile
	}

	audioMessage, ok := msg.(*waProto.AudioMessage)
	if ok {
		var waveform []int
		if audioMessage.Waveform != nil {
			waveform = make([]int, len(audioMessage.Waveform))
			maxWave := 0
			for i, part := range audioMessage.Waveform {
				waveform[i] = int(part)
				if waveform[i] > maxWave {
					maxWave = waveform[i]
				}
			}
			multiplier := 0
			if maxWave > 0 {
				multiplier = 1024 / maxWave
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
		if audioMessage.GetPtt() || audioMessage.GetMimetype() == "audio/ogg; codecs/opus" {
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

		portal.bridge.Formatter.ParseWhatsApp(ctx, portal.MXID, captionContent, msg.GetContextInfo().GetMentionedJid(), false, false)
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

func (portal *Portal) uploadMedia(ctx context.Context, intent *appservice.IntentAPI, data []byte, content *event.MessageEventContent) error {
	uploadMimeType, file := portal.encryptFileInPlace(data, content.Info.MimeType)

	req := mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  uploadMimeType,
	}
	var mxc id.ContentURI
	if portal.bridge.Config.Homeserver.AsyncMedia {
		uploaded, err := intent.UploadAsync(ctx, req)
		if err != nil {
			return err
		}
		mxc = uploaded.ContentURI
	} else {
		uploaded, err := intent.UploadMedia(ctx, req)
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

func (portal *Portal) convertMediaMessage(ctx context.Context, intent *appservice.IntentAPI, source *User, info *types.MessageInfo, msg MediaMessage, typeName string, isBackfill bool) *ConvertedMessage {
	converted := portal.convertMediaMessageContent(ctx, intent, msg)
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
		zerolog.Ctx(ctx).Debug().Msg("No URL present error for media message, ignoring...")
		return nil
	} else if errors.Is(err, whatsmeow.ErrFileLengthMismatch) || errors.Is(err, whatsmeow.ErrInvalidMediaSHA256) {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Mismatching media checksums in message. Ignoring because WhatsApp seems to ignore them too")
	} else if err != nil {
		return portal.makeMediaBridgeFailureMessage(info, err, converted, nil, "")
	}

	err = portal.uploadMedia(ctx, intent, data, converted.Content)
	if err != nil {
		if errors.Is(err, mautrix.MTooLarge) {
			return portal.makeMediaBridgeFailureMessage(info, errors.New("homeserver rejected too large file"), converted, nil, "")
		} else if httpErr := (mautrix.HTTPError{}); errors.As(err, &httpErr) && httpErr.IsStatus(413) {
			return portal.makeMediaBridgeFailureMessage(info, errors.New("proxy rejected too large file"), converted, nil, "")
		} else {
			return portal.makeMediaBridgeFailureMessage(info, fmt.Errorf("failed to upload media: %w", err), converted, nil, "")
		}
	}
	return converted
}

func (portal *Portal) fetchMediaRetryEvent(ctx context.Context, msg *database.Message) (*FailedMediaMeta, error) {
	errorMeta, ok := portal.mediaErrorCache[msg.JID]
	if ok {
		return errorMeta, nil
	}
	evt, err := portal.MainIntent().GetEvent(ctx, portal.MXID, msg.MXID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch event %s: %w", msg.MXID, err)
	}
	if evt.Type == event.EventEncrypted {
		err = evt.Content.ParseRaw(evt.Type)
		if err != nil {
			return nil, fmt.Errorf("failed to parse encrypted content in %s: %w", msg.MXID, err)
		}
		evt, err = portal.bridge.Crypto.Decrypt(ctx, evt)
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

func (portal *Portal) sendMediaRetryFailureEdit(ctx context.Context, intent *appservice.IntentAPI, msg *database.Message, err error) {
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
	resp, sendErr := portal.sendMessage(ctx, intent, event.EventMessage, &content, nil, time.Now().UnixMilli())
	if sendErr != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to edit message after media retry failure")
	} else {
		zerolog.Ctx(ctx).Debug().Stringer("edit_mxid", resp.EventID).
			Msg("Successfully edited message after media retry failure")
	}
}

func (portal *Portal) handleMediaRetry(retry *events.MediaRetry, source *User) {
	log := portal.zlog.With().
		Str("action", "handle media retry").
		Str("retry_message_id", retry.MessageID).
		Logger()
	ctx := log.WithContext(context.TODO())
	msg, err := portal.bridge.DB.Message.GetByJID(ctx, portal.Key, retry.MessageID)
	if msg == nil {
		log.Warn().Msg("Dropping media retry notification for unknown message")
		return
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Stringer("retry_message_mxid", msg.MXID)
	})
	if msg.Error != database.MsgErrMediaNotFound {
		log.Warn().Msg("Dropping media retry notification for non-errored message")
		return
	}

	meta, err := portal.fetchMediaRetryEvent(ctx, msg)
	if err != nil {
		log.Warn().Err(err).Msg("Can't handle media retry notification for message")
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
	if puppet == nil {
		// TODO handle lids?
		return
	}
	intent := puppet.IntentFor(portal)

	retryData, err := whatsmeow.DecryptMediaRetryNotification(retry, meta.Media.Key)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to decrypt media retry notification")
		portal.sendMediaRetryFailureEdit(ctx, intent, msg, err)
		return
	} else if retryData.GetResult() != waProto.MediaRetryNotification_SUCCESS {
		errorName := waProto.MediaRetryNotification_ResultType_name[int32(retryData.GetResult())]
		if retryData.GetDirectPath() == "" {
			log.Warn().Str("error_name", errorName).Msg("Got error response in media retry notification")
			log.Debug().Any("error_content", retryData).Msg("Full error response content")
			if retryData.GetResult() == waProto.MediaRetryNotification_NOT_FOUND {
				portal.sendMediaRetryFailureEdit(ctx, intent, msg, whatsmeow.ErrMediaNotAvailableOnPhone)
			} else {
				portal.sendMediaRetryFailureEdit(ctx, intent, msg, fmt.Errorf("phone sent error response: %s", errorName))
			}
			return
		} else {
			log.Debug().Msg("Got error response in media retry notification, but response also contains a new download URL - trying to download")
		}
	}

	data, err := source.Client.DownloadMediaWithPath(retryData.GetDirectPath(), meta.Media.EncSHA256, meta.Media.SHA256, meta.Media.Key, meta.Media.Length, meta.Media.Type, "")
	if err != nil {
		log.Warn().Err(err).Msg("Failed to download media after retry notification")
		portal.sendMediaRetryFailureEdit(ctx, intent, msg, err)
		return
	}
	err = portal.uploadMedia(ctx, intent, data, meta.Content)
	if err != nil {
		log.Err(err).Msg("Failed to re-upload media after retry notification")
		portal.sendMediaRetryFailureEdit(ctx, intent, msg, fmt.Errorf("re-uploading media failed: %v", err))
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
		"m.new_content": maps.Clone(meta.ExtraContent),
	}
	resp, err := portal.sendMessage(ctx, intent, meta.Type, replaceContent, meta.ExtraContent, time.Now().UnixMilli())
	if err != nil {
		log.Err(err).Msg("Failed to edit message after reuploading media from retry notification")
		return
	}
	log.Debug().Stringer("edit_mxid", resp.EventID).Msg("Successfully edited message after retry notification")
	err = msg.UpdateMXID(ctx, resp.EventID, database.MsgNormal, database.MsgNoError)
	if err != nil {
		log.Err(err).Msg("Failed to save message to database after editing with retry notification")
	}
}

func (portal *Portal) requestMediaRetry(ctx context.Context, user *User, eventID id.EventID, mediaKey []byte) (bool, error) {
	log := zerolog.Ctx(ctx).With().Stringer("target_event_id", eventID).Logger()
	msg, err := portal.bridge.DB.Message.GetByMXID(ctx, eventID)
	if err != nil {
		log.Err(err).Msg("Failed to get media retry target from database")
		return false, fmt.Errorf("failed to get media retry target")
	} else if msg == nil {
		log.Debug().Msg("Can't send media retry request for unknown message")
		return false, fmt.Errorf("unknown message")
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("target_message_id", msg.JID)
	})
	if msg.Error != database.MsgErrMediaNotFound {
		log.Debug().Msg("Dropping media retry request for non-errored message")
		return false, fmt.Errorf("message is not errored")
	}

	// If the media key is not provided, grab it from the event in Matrix
	if mediaKey == nil {
		evt, err := portal.fetchMediaRetryEvent(ctx, msg)
		if err != nil {
			log.Warn().Err(err).Msg("Dropping media retry request as media key couldn't be fetched")
			return true, nil
		}
		mediaKey = evt.Media.Key
	}

	err = user.Client.SendMediaRetryReceipt(&types.MessageInfo{
		ID: msg.JID,
		MessageSource: types.MessageSource{
			IsFromMe: msg.Sender.User == user.JID.User,
			IsGroup:  !portal.IsPrivateChat(),
			Sender:   msg.Sender,
			Chat:     portal.Key.JID,
		},
	}, mediaKey)
	if err != nil {
		log.Err(err).Msg("Failed to send media retry request")
	} else {
		log.Debug().Msg("Sent media retry request")
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
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Malformed thumbnail URL in event, falling back to generating thumbnail from source")
	} else if thumbnail, err := portal.MainIntent().DownloadBytes(ctx, mxc); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to download thumbnail in event, falling back to generating thumbnail from source")
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
	if err = cwebp.Encode(&webpBuffer, decodedImg, nil); err != nil {
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
		caption, mentionedJIDs = portal.bridge.Formatter.ParseMatrix(content.FormattedBody, content.Mentions)
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
	data, err := portal.MainIntent().DownloadBytes(ctx, mxc)
	if err != nil {
		return nil, exerrors.NewDualError(errMediaDownloadFailed, err)
	}
	if file != nil {
		err = file.DecryptInPlace(data)
		if err != nil {
			return nil, exerrors.NewDualError(errMediaDecryptFailed, err)
		}
	}
	mimeType := content.GetInfo().MimeType
	if mimeType == "" {
		content.Info.MimeType = "application/octet-stream"
	}
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
			return nil, exerrors.NewDualError(fmt.Errorf("%w (%s to %s)", errMediaConvertFailed, mimeType, content.Info.MimeType), convertErr)
		} else {
			// If the mime type didn't change and the errored conversion function returned the original data, just log a warning and continue
			zerolog.Ctx(ctx).Warn().Err(convertErr).Str("source_mime", mimeType).Msg("Failed to re-encode media, continuing with original file")
		}
	}
	var uploadResp whatsmeow.UploadResponse
	if portal.Key.JID.Server == types.NewsletterServer {
		uploadResp, err = sender.Client.UploadNewsletter(ctx, data, mediaType)
	} else {
		uploadResp, err = sender.Client.Upload(ctx, data, mediaType)
	}
	if err != nil {
		return nil, exerrors.NewDualError(errMediaWhatsAppUploadFailed, err)
	}

	// Audio doesn't have thumbnails
	var thumbnail []byte
	if mediaType != whatsmeow.MediaAudio {
		thumbnail, err = portal.downloadThumbnail(ctx, data, content.GetInfo().ThumbnailURL, eventID, isSticker)
		// Ignore format errors for non-image files, we don't care about those thumbnails
		if err != nil && (!errors.Is(err, image.ErrFormat) || mediaType == whatsmeow.MediaImage) {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to generate thumbnail for image message")
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

func (portal *Portal) addRelaybotFormat(ctx context.Context, userID id.UserID, content *event.MessageEventContent) bool {
	member := portal.MainIntent().Member(ctx, portal.MXID, userID)
	if member == nil {
		member = &event.MemberEventContent{}
	}
	content.EnsureHasHTML()
	data, err := portal.bridge.Config.Bridge.Relay.FormatMessage(content, userID, *member)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to apply relaybot format")
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
			return portal.bridge.Formatter.ParseMatrix(msg.HTML, nil)
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

func (portal *Portal) convertMatrixPollVote(ctx context.Context, sender *User, evt *event.Event) (*waProto.Message, *User, *extraConvertMeta, error) {
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
	log := zerolog.Ctx(ctx)
	pollMsg, err := portal.bridge.DB.Message.GetByMXID(ctx, content.RelatesTo.EventID)
	if err != nil {
		log.Err(err).Msg("Failed to get poll message from database")
		return nil, sender, nil, fmt.Errorf("failed to get poll message")
	} else if pollMsg == nil {
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
		mappedAnswers, err := pollMsg.GetPollOptionHashes(ctx, answers)
		if err != nil {
			log.Err(err).Msg("Failed to get poll option hashes from database")
			return nil, sender, nil, fmt.Errorf("failed to get poll option hashes")
		}
		for _, selection := range answers {
			hash, ok := mappedAnswers[selection]
			if ok {
				optionHashes = append(optionHashes, hash[:])
			} else {
				log.Warn().Str("option", selection).Msg("Didn't find hash for selected option")
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

func (portal *Portal) convertMatrixPollStart(ctx context.Context, sender *User, evt *event.Event) (*waProto.Message, *User, *extraConvertMeta, error) {
	content, ok := evt.Content.Parsed.(*PollStartContent)
	if !ok {
		return nil, sender, nil, fmt.Errorf("%w %T", errUnexpectedParsedContentType, evt.Content.Parsed)
	}
	maxAnswers := content.PollStart.MaxSelections
	if maxAnswers >= len(content.PollStart.Answers) || maxAnswers < 0 {
		maxAnswers = 0
	}
	ctxInfo := portal.generateContextInfo(ctx, content.RelatesTo)
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
			zerolog.Ctx(ctx).Warn().Str("option", body).Msg("Poll has duplicate options, rejecting")
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

func (portal *Portal) generateContextInfo(ctx context.Context, relatesTo *event.RelatesTo) *waProto.ContextInfo {
	var ctxInfo waProto.ContextInfo
	replyToID := relatesTo.GetReplyTo()
	if len(replyToID) > 0 {
		replyToMsg, err := portal.bridge.DB.Message.GetByMXID(ctx, replyToID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("reply_to_mxid", replyToID).
				Msg("Failed to get reply target from database")
		}
		if replyToMsg != nil && !replyToMsg.IsFakeJID() && (replyToMsg.Type == database.MsgNormal || replyToMsg.Type == database.MsgMatrixPoll || replyToMsg.Type == database.MsgBeeperGallery) {
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

	GalleryExtraParts []*waProto.Message

	MediaHandle string
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
	realSenderMXID := sender.MXID
	isRelay := false
	if !sender.IsLoggedIn() || (portal.IsPrivateChat() && sender.JID.User != portal.Key.Receiver.User) {
		if !portal.HasRelaybot() {
			return nil, sender, extraMeta, errUserNotLoggedIn
		}
		sender = portal.GetRelayUser()
		if !sender.IsLoggedIn() {
			return nil, sender, extraMeta, errRelaybotNotLoggedIn
		}
		isRelay = true
	}
	log := zerolog.Ctx(ctx)
	var editRootMsg *database.Message
	if editEventID := content.RelatesTo.GetReplaceID(); editEventID != "" {
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Stringer("edit_target_mxid", editEventID)
		})
		var err error
		editRootMsg, err = portal.bridge.DB.Message.GetByMXID(ctx, editEventID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get edit target message from database")
			return nil, sender, extraMeta, errEditUnknownTarget
		} else if editErr := getEditError(editRootMsg, sender); editErr != nil {
			return nil, sender, extraMeta, editErr
		}
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Str("edit_target_id", editRootMsg.JID)
		})
		extraMeta.EditRootMsg = editRootMsg
		if content.NewContent != nil {
			content = content.NewContent
		}
	}

	msg := &waProto.Message{}
	ctxInfo := portal.generateContextInfo(ctx, content.RelatesTo)
	relaybotFormatted := isRelay && portal.addRelaybotFormat(ctx, realSenderMXID, content)
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
	if content.MsgType == event.MsgAudio && content.FileName != "" && content.Body != content.FileName {
		// Send audio messages with captions as files since WhatsApp doesn't support captions on audio messages
		content.MsgType = event.MsgFile
	}

	switch content.MsgType {
	case event.MsgText, event.MsgEmote, event.MsgNotice:
		text := content.Body
		if content.MsgType == event.MsgNotice && !portal.bridge.Config.Bridge.BridgeNotices {
			return nil, sender, extraMeta, errMNoticeDisabled
		}
		if content.Format == event.FormatHTML {
			text, ctxInfo.MentionedJid = portal.bridge.Formatter.ParseMatrix(content.FormattedBody, content.Mentions)
		}
		if content.MsgType == event.MsgEmote && !relaybotFormatted {
			text = "/me " + text
		}
		msg.ExtendedTextMessage = &waProto.ExtendedTextMessage{
			Text:        &text,
			ContextInfo: ctxInfo,
		}
		hasPreview := portal.convertURLPreviewToWhatsApp(ctx, sender, content, msg.ExtendedTextMessage)
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
		extraMeta.MediaHandle = media.Handle
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
	case event.MsgBeeperGallery:
		if isRelay {
			return nil, sender, extraMeta, errGalleryRelay
		} else if content.BeeperGalleryCaption != "" {
			return nil, sender, extraMeta, errGalleryCaption
		} else if portal.Key.JID.Server == types.NewsletterServer {
			// We don't handle the media handles properly for multiple messages
			return nil, sender, extraMeta, fmt.Errorf("can't send gallery to newsletter")
		}
		for i, part := range content.BeeperGalleryImages {
			// TODO support videos
			media, err := portal.preprocessMatrixMedia(ctx, sender, false, part, evt.ID, whatsmeow.MediaImage)
			if media == nil {
				return nil, sender, extraMeta, fmt.Errorf("failed to handle image #%d: %w", i+1, err)
			}
			imageMsg := &waProto.ImageMessage{
				ContextInfo:   ctxInfo,
				JpegThumbnail: media.Thumbnail,
				Url:           &media.URL,
				DirectPath:    &media.DirectPath,
				MediaKey:      media.MediaKey,
				Mimetype:      &part.GetInfo().MimeType,
				FileEncSha256: media.FileEncSHA256,
				FileSha256:    media.FileSHA256,
				FileLength:    proto.Uint64(uint64(media.FileLength)),
			}
			if i == 0 {
				msg.ImageMessage = imageMsg
			} else {
				extraMeta.GalleryExtraParts = append(extraMeta.GalleryExtraParts, &waProto.Message{
					ImageMessage: imageMsg,
				})
			}
		}
	case event.MessageType(event.EventSticker.Type):
		media, err := portal.preprocessMatrixMedia(ctx, sender, relaybotFormatted, content, evt.ID, whatsmeow.MediaImage)
		if media == nil {
			return nil, sender, extraMeta, err
		}
		extraMeta.MediaHandle = media.Handle
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
		extraMeta.MediaHandle = media.Handle
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
		extraMeta.MediaHandle = media.Handle
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
		extraMeta.MediaHandle = media.Handle
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
		ID:        sender.Client.GenerateMessageID(),
		Timestamp: time.Now(),
		MessageSource: types.MessageSource{
			Sender:   sender.JID,
			Chat:     portal.Key.JID,
			IsFromMe: true,
			IsGroup:  portal.Key.JID.Server == types.GroupServer || portal.Key.JID.Server == types.BroadcastServer,
		},
	}
}

func (portal *Portal) HandleMatrixMessage(ctx context.Context, sender *User, evt *event.Event, timings messageTimings) {
	start := time.Now()
	ms := metricSender{portal: portal, timings: &timings}
	log := zerolog.Ctx(ctx)

	allowRelay := evt.Type != TypeMSC3381PollResponse && evt.Type != TypeMSC3381V2PollResponse && evt.Type != TypeMSC3381PollStart
	if err := portal.canBridgeFrom(sender, allowRelay, true); err != nil {
		go ms.sendMessageMetrics(ctx, evt, err, "Ignoring", true)
		return
	} else if portal.Key.JID == types.StatusBroadcastJID && portal.bridge.Config.Bridge.DisableStatusBroadcastSend {
		go ms.sendMessageMetrics(ctx, evt, errBroadcastSendDisabled, "Ignoring", true)
		return
	}

	messageAge := timings.totalReceive
	origEvtID := evt.ID
	var dbMsg *database.Message
	if retryMeta := evt.Content.AsMessage().MessageSendRetry; retryMeta != nil {
		origEvtID = retryMeta.OriginalEventID
		var err error
		logEvt := log.Debug().
			Dur("message_age", messageAge).
			Int("retry_count", retryMeta.RetryCount).
			Stringer("orig_event_id", origEvtID)
		dbMsg, err = portal.bridge.DB.Message.GetByMXID(ctx, origEvtID)
		if err != nil {
			log.Err(err).Msg("Failed to get retry request target message from database")
			// TODO drop message?
		} else if dbMsg != nil && dbMsg.Sent {
			logEvt.
				Str("wa_message_id", dbMsg.JID).
				Msg("Ignoring retry request as message was already sent")
			go ms.sendMessageMetrics(ctx, evt, nil, "", true)
			return
		} else if dbMsg != nil {
			logEvt.
				Str("wa_message_id", dbMsg.JID).
				Msg("Got retry request for message")
		} else {
			logEvt.Msg("Got retry request for message, but original message is not known")
		}
	} else {
		log.Debug().Dur("message_age", messageAge).Msg("Received Matrix message")
	}

	errorAfter := portal.bridge.Config.Bridge.MessageHandlingTimeout.ErrorAfter
	deadline := portal.bridge.Config.Bridge.MessageHandlingTimeout.Deadline
	isScheduled, _ := evt.Content.Raw["com.beeper.scheduled"].(bool)
	if isScheduled {
		log.Debug().Msg("Message is a scheduled message, extending handling timeouts")
		errorAfter *= 10
		deadline *= 10
	}

	if errorAfter > 0 {
		remainingTime := errorAfter - messageAge
		if remainingTime < 0 {
			go ms.sendMessageMetrics(ctx, evt, errTimeoutBeforeHandling, "Timeout handling", true)
			return
		} else if remainingTime < 1*time.Second {
			log.Warn().
				Dur("remaining_timeout", remainingTime).
				Dur("warning_total_timeout", errorAfter).
				Msg("Message was delayed before reaching the bridge")
		}
		go func() {
			time.Sleep(remainingTime)
			ms.sendMessageMetrics(ctx, evt, errMessageTakingLong, "Timeout handling", false)
		}()
	}

	timedCtx := ctx
	if deadline > 0 {
		var cancel context.CancelFunc
		timedCtx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}

	timings.preproc = time.Since(start)
	start = time.Now()
	msg, sender, extraMeta, err := portal.convertMatrixMessage(timedCtx, sender, evt)
	timings.convert = time.Since(start)
	if msg == nil {
		go ms.sendMessageMetrics(ctx, evt, err, "Error converting", true)
		return
	}
	if extraMeta == nil {
		extraMeta = &extraConvertMeta{}
	}
	dbMsgType := database.MsgNormal
	if msg.PollCreationMessage != nil || msg.PollCreationMessageV2 != nil || msg.PollCreationMessageV3 != nil {
		dbMsgType = database.MsgMatrixPoll
	} else if msg.EditedMessage == nil {
		portal.MarkDisappearing(ctx, origEvtID, time.Duration(portal.ExpirationTime)*time.Second, time.Now())
	} else {
		dbMsgType = database.MsgEdit
	}
	info := portal.generateMessageInfo(sender)
	if dbMsg == nil {
		dbMsg = portal.markHandled(ctx, nil, info, evt.ID, evt.Sender, false, true, dbMsgType, 0, database.MsgNoError)
	} else {
		info.ID = dbMsg.JID
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Str("wa_message_id", info.ID)
	})
	if dbMsgType == database.MsgMatrixPoll && extraMeta.PollOptions != nil {
		err = dbMsg.PutPollOptions(ctx, extraMeta.PollOptions)
		if err != nil {
			log.Err(err).Msg("Failed to save poll options in message to database")
		}
	}
	log.Debug().Msg("Sending Matrix event to WhatsApp")
	start = time.Now()
	resp, err := sender.Client.SendMessage(timedCtx, portal.Key.JID, msg, whatsmeow.SendRequestExtra{
		ID:          info.ID,
		MediaHandle: extraMeta.MediaHandle,
	})
	timings.totalSend = time.Since(start)
	timings.whatsmeow = resp.DebugTimings
	if err != nil {
		go ms.sendMessageMetrics(ctx, evt, err, "Error sending", true)
		return
	}
	err = dbMsg.MarkSent(ctx, resp.Timestamp)
	if err != nil {
		log.Err(err).Msg("Failed to mark message as sent in database")
	}
	if extraMeta != nil && len(extraMeta.GalleryExtraParts) > 0 {
		for i, part := range extraMeta.GalleryExtraParts {
			partInfo := portal.generateMessageInfo(sender)
			partDBMsg := portal.markHandled(ctx, nil, partInfo, evt.ID, evt.Sender, false, true, database.MsgBeeperGallery, i+1, database.MsgNoError)
			log.Debug().Int("part_index", i+1).Str("wa_part_message_id", partInfo.ID).Msg("Sending gallery part to WhatsApp")
			resp, err = sender.Client.SendMessage(timedCtx, portal.Key.JID, part, whatsmeow.SendRequestExtra{ID: partInfo.ID})
			if err != nil {
				go ms.sendMessageMetrics(ctx, evt, err, "Error sending", true)
				return
			}
			log.Debug().Int("part_index", i+1).Str("wa_part_message_id", partInfo.ID).Msg("Sent gallery part to WhatsApp")
			err = partDBMsg.MarkSent(ctx, resp.Timestamp)
			if err != nil {
				log.Err(err).
					Str("part_id", partInfo.ID).
					Msg("Failed to mark gallery extra part as sent in database")
			}
		}
	}
	go ms.sendMessageMetrics(ctx, evt, nil, "", true)
}

func (portal *Portal) HandleMatrixReaction(ctx context.Context, sender *User, evt *event.Event) {
	log := zerolog.Ctx(ctx)
	if err := portal.canBridgeFrom(sender, false, true); err != nil {
		go portal.sendMessageMetrics(ctx, evt, err, "Ignoring", nil)
		return
	} else if portal.Key.JID.Server == types.BroadcastServer {
		// TODO implement this, probably by only sending the reaction to the sender of the status message?
		//      (whatsapp hasn't published the feature yet)
		go portal.sendMessageMetrics(ctx, evt, errBroadcastReactionNotSupported, "Ignoring", nil)
		return
	}

	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if ok && strings.Contains(content.RelatesTo.Key, "retry") || strings.HasPrefix(content.RelatesTo.Key, "\u267b") { // ♻️
		if retryRequested, _ := portal.requestMediaRetry(ctx, sender, content.RelatesTo.EventID, nil); retryRequested {
			_, _ = portal.MainIntent().RedactEvent(ctx, portal.MXID, evt.ID, mautrix.ReqRedact{
				Reason: "requested media from phone",
			})
			// Errored media, don't try to send as reaction
			return
		}
	}

	log.Debug().Msg("Received Matrix reaction event")
	err := portal.handleMatrixReaction(ctx, sender, evt)
	go portal.sendMessageMetrics(ctx, evt, err, "Error sending", nil)
}

func (portal *Portal) handleMatrixReaction(ctx context.Context, sender *User, evt *event.Event) error {
	log := zerolog.Ctx(ctx)
	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok {
		return fmt.Errorf("unexpected parsed content type %T", evt.Content.Parsed)
	}
	log.UpdateContext(func(c zerolog.Context) zerolog.Context {
		return c.Stringer("target_event_id", content.RelatesTo.EventID)
	})
	target, err := portal.bridge.DB.Message.GetByMXID(ctx, content.RelatesTo.EventID)
	if err != nil {
		log.Err(err).Msg("Failed to get target message from database")
		return fmt.Errorf("failed to get target event")
	} else if target == nil || target.Type == database.MsgReaction {
		return fmt.Errorf("unknown target event %s", content.RelatesTo.EventID)
	}
	info := portal.generateMessageInfo(sender)
	dbMsg := portal.markHandled(ctx, nil, info, evt.ID, evt.Sender, false, true, database.MsgReaction, 0, database.MsgNoError)
	portal.upsertReaction(ctx, nil, target.JID, sender.JID, evt.ID, info.ID)
	log.Debug().Str("whatsapp_reaction_id", info.ID).Msg("Sending Matrix reaction to WhatsApp")
	resp, err := portal.sendReactionToWhatsApp(sender, info.ID, target, content.RelatesTo.Key, evt.Timestamp)
	if err == nil {
		err = dbMsg.MarkSent(ctx, resp.Timestamp)
	}
	return err
}

func (portal *Portal) sendReactionToWhatsApp(sender *User, id types.MessageID, target *database.Message, key string, timestamp int64) (whatsmeow.SendResponse, error) {
	var messageKeyParticipant *string
	if !portal.IsPrivateChat() {
		messageKeyParticipant = proto.String(target.Sender.ToNonAD().String())
	}
	key = variationselector.Remove(key)
	ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
	defer cancel()
	return sender.Client.SendMessage(ctx, portal.Key.JID, &waProto.Message{
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

func (portal *Portal) upsertReaction(ctx context.Context, intent *appservice.IntentAPI, targetJID types.MessageID, senderJID types.JID, mxid id.EventID, jid types.MessageID) {
	log := zerolog.Ctx(ctx)
	dbReaction, err := portal.bridge.DB.Reaction.GetByTargetJID(ctx, portal.Key, targetJID, senderJID)
	if err != nil {
		log.Err(err).Msg("Failed to get existing reaction from database for upsert")
		return
	}
	if dbReaction == nil {
		dbReaction = portal.bridge.DB.Reaction.New()
		dbReaction.Chat = portal.Key
		dbReaction.TargetJID = targetJID
		dbReaction.Sender = senderJID
	} else if intent != nil {
		log.Debug().
			Stringer("old_reaction_mxid", dbReaction.MXID).
			Msg("Redacting old Matrix reaction after new one was sent")
		if intent != nil {
			_, err = intent.RedactEvent(ctx, portal.MXID, dbReaction.MXID)
		}
		if intent == nil || errors.Is(err, mautrix.MForbidden) {
			_, err = portal.MainIntent().RedactEvent(ctx, portal.MXID, dbReaction.MXID)
		}
		if err != nil {
			log.Err(err).
				Stringer("old_reaction_mxid", dbReaction.MXID).
				Msg("Failed to redact old reaction")
		}
	}
	dbReaction.MXID = mxid
	dbReaction.JID = jid
	err = dbReaction.Upsert(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to upsert reaction to database")
	}
}

func (portal *Portal) HandleMatrixRedaction(ctx context.Context, sender *User, evt *event.Event) {
	log := zerolog.Ctx(ctx)
	if err := portal.canBridgeFrom(sender, true, true); err != nil {
		go portal.sendMessageMetrics(ctx, evt, err, "Ignoring", nil)
		return
	}
	log.Debug().Msg("Received Matrix redaction")

	senderLogIdentifier := sender.MXID
	if !sender.HasSession() {
		sender = portal.GetRelayUser()
		senderLogIdentifier += " (through relaybot)"
	}

	msg, err := portal.bridge.DB.Message.GetByMXID(ctx, evt.Redacts)
	if err != nil {
		log.Err(err).Msg("Failed to get redaction target event from database")
		go portal.sendMessageMetrics(ctx, evt, errTargetNotFound, "Ignoring", nil)
	} else if msg == nil {
		go portal.sendMessageMetrics(ctx, evt, errTargetNotFound, "Ignoring", nil)
	} else if msg.IsFakeJID() {
		go portal.sendMessageMetrics(ctx, evt, errTargetIsFake, "Ignoring", nil)
	} else if portal.Key.JID == types.StatusBroadcastJID && portal.bridge.Config.Bridge.DisableStatusBroadcastSend {
		go portal.sendMessageMetrics(ctx, evt, errBroadcastSendDisabled, "Ignoring", nil)
	} else if msg.Type == database.MsgReaction {
		if msg.Sender.User != sender.JID.User {
			go portal.sendMessageMetrics(ctx, evt, errReactionSentBySomeoneElse, "Ignoring", nil)
		} else if reaction, err := portal.bridge.DB.Reaction.GetByMXID(ctx, evt.Redacts); err != nil {
			log.Err(err).Msg("Failed to get target reaction from database")
			go portal.sendMessageMetrics(ctx, evt, errReactionDatabaseNotFound, "Ignoring", nil)
		} else if reaction == nil {
			go portal.sendMessageMetrics(ctx, evt, errReactionDatabaseNotFound, "Ignoring", nil)
		} else if reactionTarget, err := portal.bridge.DB.Message.GetByJID(ctx, reaction.Chat, reaction.TargetJID); err != nil {
			log.Err(err).Msg("Failed to get target reaction's target message from database")
			go portal.sendMessageMetrics(ctx, evt, errReactionTargetNotFound, "Ignoring", nil)
		} else if reactionTarget == nil {
			go portal.sendMessageMetrics(ctx, evt, errReactionTargetNotFound, "Ignoring", nil)
		} else {
			log.Debug().Str("reaction_target_message_id", msg.JID).Msg("Sending redaction of reaction to WhatsApp")
			_, err = portal.sendReactionToWhatsApp(sender, "", reactionTarget, "", evt.Timestamp)
			go portal.sendMessageMetrics(ctx, evt, err, "Error sending", nil)
		}
	} else {
		key := &waProto.MessageKey{
			FromMe:    proto.Bool(true),
			Id:        proto.String(msg.JID),
			RemoteJid: proto.String(portal.Key.JID.String()),
		}
		if msg.Sender.User != sender.JID.User {
			if portal.IsPrivateChat() {
				go portal.sendMessageMetrics(ctx, evt, errDMSentByOtherUser, "Ignoring", nil)
				return
			}
			key.FromMe = proto.Bool(false)
			key.Participant = proto.String(msg.Sender.ToNonAD().String())
		}
		log.Debug().Str("target_message_id", msg.JID).Msg("Sending redaction of message to WhatsApp")
		timedCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		_, err = sender.Client.SendMessage(timedCtx, portal.Key.JID, &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_REVOKE.Enum(),
				Key:  key,
			},
		})
		go portal.sendMessageMetrics(ctx, evt, err, "Error sending", nil)
	}
}

func (portal *Portal) HandleMatrixReadReceipt(sender bridge.User, eventID id.EventID, receipt event.ReadReceipt) {
	log := portal.zlog.With().
		Str("action", "handle matrix read receipt").
		Stringer("event_id", eventID).
		Stringer("user_id", sender.GetMXID()).
		Logger()
	ctx := log.WithContext(context.TODO())
	portal.handleMatrixReadReceipt(ctx, sender.(*User), eventID, receipt.Timestamp, true)
}

func (portal *Portal) handleMatrixReadReceipt(ctx context.Context, sender *User, eventID id.EventID, receiptTimestamp time.Time, isExplicit bool) {
	log := zerolog.Ctx(ctx).With().
		Stringer("sender_jid", sender.JID).
		Logger()
	if !sender.IsLoggedIn() {
		if isExplicit {
			log.Debug().Msg("Ignoring read receipt: user is not connected to WhatsApp")
		}
		return
	}

	maxTimestamp := receiptTimestamp
	// Implicit read receipts don't have an event ID that's already bridged
	if isExplicit {
		if message, err := portal.bridge.DB.Message.GetByMXID(ctx, eventID); err != nil {
			log.Err(err).Msg("Failed to get read receipt target message")
		} else if message != nil {
			maxTimestamp = message.Timestamp
		}
	}

	prevTimestamp := sender.GetLastReadTS(ctx, portal.Key)
	lastReadIsZero := false
	if prevTimestamp.IsZero() {
		prevTimestamp = maxTimestamp.Add(-2 * time.Second)
		lastReadIsZero = true
	}

	messages, err := portal.bridge.DB.Message.GetMessagesBetween(ctx, portal.Key, prevTimestamp, maxTimestamp)
	if err != nil {
		log.Err(err).Msg("Failed to get messages that need receipts")
		return
	}
	if len(messages) > 0 {
		sender.SetLastReadTS(ctx, portal.Key, messages[len(messages)-1].Timestamp)
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
		log.Debug().
			Bool("explicit", isExplicit).
			Time("last_read", prevTimestamp).
			Bool("last_read_is_zero", lastReadIsZero).
			Any("receipts", groupedMessages).
			Msg("Sending read receipts to WhatsApp")
	}
	for messageSender, ids := range groupedMessages {
		chatJID := portal.Key.JID
		if messageSender.Server == types.BroadcastServer {
			chatJID = messageSender
			messageSender = portal.Key.JID
		}
		err = sender.Client.MarkRead(ids, receiptTimestamp, chatJID, messageSender)
		if err != nil {
			log.Err(err).
				Array("message_ids", exzerolog.ArrayOfStrs(ids)).
				Stringer("target_user_jid", messageSender).
				Msg("Failed to send read receipt")
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
		portal.zlog.Debug().
			Stringer("user_jid", user.JID).
			Stringer("user_mxid", user.MXID).
			Str("state", string(state)).
			Msg("Bridging typing change to chat presence")
		err := user.Client.SendChatPresence(portal.Key.JID, state, types.ChatPresenceMediaText)
		if err != nil {
			portal.zlog.Err(err).
				Stringer("user_jid", user.JID).
				Stringer("user_mxid", user.MXID).
				Str("state", string(state)).
				Msg("Failed to send chat presence")
		}
		if portal.bridge.Config.Bridge.SendPresenceOnTyping {
			err = user.Client.SendPresence(types.PresenceAvailable)
			if err != nil {
				user.zlog.Warn().Err(err).Msg("Failed to set presence on typing")
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

func (portal *Portal) canBridgeFrom(sender *User, allowRelay, reconnectWait bool) error {
	if !sender.IsLoggedIn() {
		if allowRelay && portal.HasRelaybot() {
			return nil
		} else if sender.Session != nil {
			return errUserNotConnected
		} else if reconnectWait {
			// If a message was received exactly during a disconnection, wait a second for the socket to reconnect
			time.Sleep(1 * time.Second)
			return portal.canBridgeFrom(sender, allowRelay, false)
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

func (portal *Portal) Delete(ctx context.Context) {
	err := portal.Portal.Delete(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to delete portal from database")
	}
	portal.bridge.portalsLock.Lock()
	delete(portal.bridge.portalsByJID, portal.Key)
	if len(portal.MXID) > 0 {
		delete(portal.bridge.portalsByMXID, portal.MXID)
	}
	portal.resetChildSpaceStatus()
	portal.bridge.portalsLock.Unlock()
}

func (portal *Portal) GetMatrixUsers(ctx context.Context) ([]id.UserID, error) {
	members, err := portal.MainIntent().JoinedMembers(ctx, portal.MXID)
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

func (portal *Portal) CleanupIfEmpty(ctx context.Context) {
	users, err := portal.GetMatrixUsers(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to get Matrix user list to determine if portal needs to be cleaned up")
		return
	}

	if len(users) == 0 {
		zerolog.Ctx(ctx).Info().Msg("Room seems to be empty, cleaning up...")
		portal.Delete(ctx)
		portal.Cleanup(ctx, false)
	}
}

func (portal *Portal) Cleanup(ctx context.Context, puppetsOnly bool) {
	if len(portal.MXID) == 0 {
		return
	}
	log := zerolog.Ctx(ctx)
	intent := portal.MainIntent()
	if portal.bridge.SpecVersions.Supports(mautrix.BeeperFeatureRoomYeeting) {
		err := intent.BeeperDeleteRoom(ctx, portal.MXID)
		if err == nil || errors.Is(err, mautrix.MNotFound) {
			return
		}
		log.Warn().Err(err).Msg("Failed to delete room using beeper yeet endpoint, falling back to normal behavior")
	}
	members, err := intent.JoinedMembers(ctx, portal.MXID)
	if err != nil {
		log.Err(err).Msg("Failed to get portal members for cleanup")
		return
	}
	for member := range members.Joined {
		if member == intent.UserID {
			continue
		}
		puppet := portal.bridge.GetPuppetByMXID(member)
		if puppet != nil {
			_, err = puppet.DefaultIntent().LeaveRoom(ctx, portal.MXID)
			if err != nil {
				log.Err(err).Stringer("puppet_mxid", puppet.MXID).Msg("Failed to leave room as puppet while cleaning up portal")
			}
		} else if !puppetsOnly {
			_, err = intent.KickUser(ctx, portal.MXID, &mautrix.ReqKickUser{UserID: member, Reason: "Deleting portal"})
			if err != nil {
				log.Err(err).Stringer("user_mxid", member).Msg("Failed to kick user while cleaning up portal")
			}
		}
	}
	_, err = intent.LeaveRoom(ctx, portal.MXID)
	if err != nil {
		log.Err(err).Msg("Failed to leave room with main intent while cleaning up portal")
	}
}

func (portal *Portal) HandleMatrixLeave(brSender bridge.User, evt *event.Event) {
	log := portal.zlog.With().
		Str("action", "handle matrix leave").
		Stringer("event_id", evt.ID).
		Stringer("user_id", brSender.GetMXID()).
		Logger()
	ctx := log.WithContext(context.TODO())
	sender := brSender.(*User)
	if portal.IsPrivateChat() {
		log.Debug().Msg("User left private chat portal, cleaning up and deleting...")
		portal.Delete(ctx)
		portal.Cleanup(ctx, false)
		return
	} else if portal.bridge.Config.Bridge.BridgeMatrixLeave {
		err := sender.Client.LeaveGroup(portal.Key.JID)
		if err != nil {
			log.Err(err).Msg("Failed to leave group")
			return
		}
		//portal.log.Infoln("Leave response:", <-resp)
	}
	portal.CleanupIfEmpty(ctx)
}

func (portal *Portal) HandleMatrixKick(brSender bridge.User, brTarget bridge.Ghost, evt *event.Event) {
	sender := brSender.(*User)
	target := brTarget.(*Puppet)
	_, err := sender.Client.UpdateGroupParticipants(portal.Key.JID, []types.JID{target.JID}, whatsmeow.ParticipantChangeRemove)
	if err != nil {
		portal.zlog.Err(err).
			Stringer("kicked_by_mxid", sender.MXID).
			Stringer("kicked_by_jid", sender.JID).
			Stringer("target_jid", target.JID).
			Msg("Failed to kick user from group")
		return
	}
	//portal.log.Infoln("Kick %s response: %s", puppet.JID, <-resp)
}

func (portal *Portal) HandleMatrixInvite(brSender bridge.User, brTarget bridge.Ghost, evt *event.Event) {
	sender := brSender.(*User)
	target := brTarget.(*Puppet)
	_, err := sender.Client.UpdateGroupParticipants(portal.Key.JID, []types.JID{target.JID}, whatsmeow.ParticipantChangeAdd)
	if err != nil {
		portal.zlog.Err(err).
			Stringer("inviter_mxid", sender.MXID).
			Stringer("inviter_jid", sender.JID).
			Stringer("target_jid", target.JID).
			Msg("Failed to add user to group")
		return
	}
	//portal.log.Infofln("Add %s response: %s", puppet.JID, <-resp)
}

func (portal *Portal) HandleMatrixMeta(brSender bridge.User, evt *event.Event) {
	sender := brSender.(*User)
	if !sender.Whitelisted || !sender.IsLoggedIn() {
		return
	}
	log := portal.zlog.With().
		Str("action", "handle matrix metadata").
		Str("event_type", evt.Type.Type).
		Stringer("event_id", evt.ID).
		Stringer("sender", sender.MXID).
		Logger()
	ctx := log.WithContext(context.TODO())

	switch content := evt.Content.Parsed.(type) {
	case *event.RoomNameEventContent:
		if content.Name == portal.Name {
			return
		}
		portal.Name = content.Name
		err := sender.Client.SetGroupName(portal.Key.JID, content.Name)
		if err != nil {
			log.Err(err).Msg("Failed to update group name")
			return
		}
	case *event.TopicEventContent:
		if content.Topic == portal.Topic {
			return
		}
		portal.Topic = content.Topic
		err := sender.Client.SetGroupTopic(portal.Key.JID, "", "", content.Topic)
		if err != nil {
			log.Err(err).Msg("Failed to update group topic")
			return
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
			data, err = portal.MainIntent().DownloadBytes(ctx, content.URL)
			if err != nil {
				log.Err(err).Stringer("mxc_uri", content.URL).Msg("Failed to download updated avatar")
				return
			}
			log.Debug().Stringer("mxc_uri", content.URL).Msg("Updating group avatar")
		} else {
			log.Debug().Msg("Removing group avatar")
		}
		newID, err := sender.Client.SetGroupPhoto(portal.Key.JID, data)
		if err != nil {
			log.Err(err).Msg("Failed to update group avatar")
			return
		}
		log.Debug().Str("avatar_id", newID).Msg("Successfully updated group avatar")
		portal.Avatar = newID
		portal.AvatarURL = content.URL
	default:
		log.Debug().Type("content_type", content).Msg("Ignoring unknown metadata event type")
		return
	}
	portal.UpdateBridgeInfo(ctx)
	err := portal.Update(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to update portal after handling metadata")
	}
}
