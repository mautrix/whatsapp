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

package connector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	WANotLoggedIn      status.BridgeStateErrorCode = "wa-not-logged-in"
	WALoggedOut        status.BridgeStateErrorCode = "wa-logged-out"
	WAMainDeviceGone   status.BridgeStateErrorCode = "wa-main-device-gone"
	WAUnknownLogout    status.BridgeStateErrorCode = "wa-unknown-logout"
	WANotConnected     status.BridgeStateErrorCode = "wa-not-connected"
	WAConnecting       status.BridgeStateErrorCode = "wa-connecting"
	WAKeepaliveTimeout status.BridgeStateErrorCode = "wa-keepalive-timeout"
	WAPhoneOffline     status.BridgeStateErrorCode = "wa-phone-offline"
	WAConnectionFailed status.BridgeStateErrorCode = "wa-connection-failed"
	WADisconnected     status.BridgeStateErrorCode = "wa-transient-disconnect"
	WAStreamReplaced   status.BridgeStateErrorCode = "wa-stream-replaced"
	WAStreamError      status.BridgeStateErrorCode = "wa-stream-error"
	WAClientOutdated   status.BridgeStateErrorCode = "wa-client-outdated"
	WATemporaryBan     status.BridgeStateErrorCode = "wa-temporary-ban"
)

func init() {
	status.BridgeStateHumanErrors.Update(status.BridgeStateErrorMap{
		WALoggedOut:        "You were logged out from another device. Relogin to continue using the bridge.",
		WANotLoggedIn:      "You're not logged into WhatsApp. Relogin to continue using the bridge.",
		WAMainDeviceGone:   "Your phone was logged out from WhatsApp. Relogin to continue using the bridge.",
		WAUnknownLogout:    "You were logged out for an unknown reason. Relogin to continue using the bridge.",
		WANotConnected:     "You're not connected to WhatsApp",
		WAConnecting:       "Reconnecting to WhatsApp...",
		WAKeepaliveTimeout: "The WhatsApp web servers are not responding. The bridge will try to reconnect.",
		WAPhoneOffline:     "Your phone hasn't been seen in over 12 days. The bridge is currently connected, but will get disconnected if you don't open the app soon.",
		WAConnectionFailed: "Connecting to the WhatsApp web servers failed.",
		WADisconnected:     "Disconnected from WhatsApp. Trying to reconnect.",
		WAClientOutdated:   "Connect failure: 405 client outdated. Bridge must be updated.",
		WAStreamReplaced:   "Stream replaced: the bridge was started in another location.",
	})
}

func (wa *WhatsAppClient) handleWAEvent(rawEvt any) (success bool) {
	log := wa.UserLogin.Log
	ctx := log.WithContext(wa.Main.Bridge.BackgroundCtx)
	wa.MC.OnWhatsAppEvent(rawEvt)

	success = true
	switch evt := rawEvt.(type) {
	case *events.Message:
		success = wa.handleWAMessage(ctx, evt)
	case *events.Receipt:
		success = wa.handleWAReceipt(ctx, evt)
	case *events.ChatPresence:
		wa.handleWAChatPresence(ctx, evt)
	case *events.UndecryptableMessage:
		success = wa.handleWAUndecryptableMessage(ctx, evt)

	case *events.CallOffer:
		success = wa.handleWACallStart(ctx, evt.GroupJID, evt.CallCreator, evt.CallCreatorAlt, evt.CallID, "", evt.Timestamp)
	case *events.CallOfferNotice:
		success = wa.handleWACallStart(ctx, evt.GroupJID, evt.CallCreator, evt.CallCreatorAlt, evt.CallID, evt.Type, evt.Timestamp)
	case *events.CallTerminate, *events.CallRelayLatency, *events.CallAccept, *events.UnknownCallEvent:
		// ignore
	case *events.IdentityChange:
		wa.handleWAIdentityChange(ctx, evt)
	case *events.MarkChatAsRead:
		success = wa.handleWAMarkChatAsRead(ctx, evt)
	case *events.DeleteForMe:
		success = wa.handleWADeleteForMe(ctx, evt)
	case *events.DeleteChat:
		success = wa.handleWADeleteChat(ctx, evt)
	case *events.Mute:
		success = wa.handleWAMute(evt)
	case *events.Archive:
		success = wa.handleWAArchive(evt)
	case *events.Pin:
		success = wa.handleWAPin(evt)

	case *events.HistorySync:
		wa.UserLogin.Log.Warn().Msg("Unexpected history sync event received")
	case *events.MediaRetry:
		success = wa.handleWAMediaRetry(ctx, evt)

	case *events.GroupInfo:
		success = wa.handleWAGroupInfoChange(ctx, evt)
	case *events.JoinedGroup:
		success = wa.handleWAJoinedGroup(ctx, evt)
	case *events.NewsletterJoin:
		success = wa.handleWANewsletterJoin(ctx, evt)
	case *events.NewsletterLeave:
		success = wa.handleWANewsletterLeave(evt)
	case *events.Picture:
		success = wa.handleWAPictureUpdate(ctx, evt)
	case *events.NotifyAccountReachoutTimelock:
		wa.UserLogin.TrackAnalytics("WhatsApp Account Reachout Timelock", map[string]any{
			"enforcement_type":      evt.EnforcementType,
			"is_active":             evt.IsActive,
			"time_enforcement_ends": evt.TimeEnforcementEnds.Time,
		})
		wa.UserLogin.Metadata.(*waid.UserLoginMetadata).ReachoutTimelockUntil = evt.TimeEnforcementEnds.Time
		if wa.UserLogin.BridgeState.GetPrevUnsent().StateEvent == status.StateConnected {
			wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		}
		err := wa.UserLogin.Save(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save user login metadata after reachout timelock update")
		}

	case *events.AppStateSyncComplete:
		wa.handleWAAppStateSyncComplete(ctx, evt)
	case *events.AppStateSyncError:
		wa.handleWAAppStateSyncError(ctx, evt)
	case *events.AppState:
		// Intentionally ignored
	case *events.PushNameSetting:
		// Send presence available when connecting and when the pushname is changed.
		// This makes sure that outgoing messages always have the right pushname.
		err := wa.updatePresence(ctx, types.PresenceUnavailable)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to send presence after push name update")
		}
		_, _, err = wa.GetStore().Contacts.PutPushName(ctx, wa.JID.ToNonAD(), evt.Action.GetName())
		if err != nil {
			log.Err(err).Msg("Failed to update push name in store")
		}
		_, _, err = wa.GetStore().Contacts.PutPushName(ctx, wa.GetLID().ToNonAD(), evt.Action.GetName())
		if err != nil {
			log.Err(err).Msg("Failed to update push name in store")
		}
		go wa.syncGhost(wa.JID.ToNonAD(), "push name setting", nil)
	case *events.Contact:
		go wa.syncGhost(evt.JID, "contact event", nil)
	case *events.PushName:
		go wa.syncGhost(evt.JID, "push name event", nil)
	case *events.BusinessName:
		go wa.syncGhost(evt.JID, "business name event", nil)

	case *events.Connected:
		log.Debug().Msg("Connected to WhatsApp socket")
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		if len(wa.GetStore().PushName) > 0 {
			go func() {
				err := wa.updatePresence(ctx, types.PresenceUnavailable)
				if err != nil {
					log.Warn().Err(err).Msg("Failed to send initial presence after connecting")
				}
			}()
			go wa.syncRemoteProfile(ctx, nil)
		}
	case *events.OfflineSyncPreview:
		log.Info().
			Int("message_count", evt.Messages).
			Int("receipt_count", evt.Receipts).
			Int("notification_count", evt.Notifications).
			Int("app_data_change_count", evt.AppDataChanges).
			Msg("Server sent number of events that were missed during downtime")
	case *events.OfflineSyncCompleted:
		if !wa.PhoneRecentlySeen(true) {
			log.Info().
				Int("evt_count", evt.Count).
				Time("phone_last_seen", wa.UserLogin.Metadata.(*waid.UserLoginMetadata).PhoneLastSeen.Time).
				Msg("Offline sync completed, but phone last seen date is still old")
		} else {
			log.Info().
				Int("evt_count", evt.Count).
				Msg("Offline sync completed")
		}
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		wa.notifyOfflineSyncWaiter(nil)
	case *events.LoggedOut:
		wa.handleWALogout(evt.Reason, evt.OnConnect)
		wa.notifyOfflineSyncWaiter(fmt.Errorf("logged out: %s", evt.Reason))
	case *events.Disconnected:
		// Don't send the normal transient disconnect state if we're already in a different transient disconnect state.
		// TODO remove this if/when the phone offline state is moved to a sub-state of CONNECTED
		if wa.UserLogin.BridgeState.GetPrev().Error != WAPhoneOffline && wa.PhoneRecentlySeen(false) {
			wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: WADisconnected})
		}
		wa.notifyOfflineSyncWaiter(fmt.Errorf("disconnected"))
	case *events.StreamError:
		var message string
		if evt.Code != "" {
			message = fmt.Sprintf("Unknown stream error with code %s", evt.Code)
		} else if children := evt.Raw.GetChildren(); len(children) > 0 {
			message = fmt.Sprintf("Unknown stream error (contains %s node)", children[0].Tag)
		} else {
			message = "Unknown stream error"
		}
		wa.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      WAStreamError,
			Message:    message,
		})
		wa.notifyOfflineSyncWaiter(fmt.Errorf("stream error: %s", message))
	case *events.StreamReplaced:
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: WAStreamReplaced})
		wa.notifyOfflineSyncWaiter(fmt.Errorf("stream replaced"))
	case *events.KeepAliveTimeout:
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: WAKeepaliveTimeout})
	case *events.KeepAliveRestored:
		log.Info().Msg("Keepalive restored after timeouts, sending connected event")
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	case *events.ConnectFailure:
		wa.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      status.BridgeStateErrorCode(fmt.Sprintf("wa-connect-failure-%d", evt.Reason)),
			Message:    fmt.Sprintf("Unknown connection failure: %s (%s)", evt.Reason, evt.Message),
		})
		wa.notifyOfflineSyncWaiter(fmt.Errorf("connection failure: %s (%s)", evt.Reason, evt.Message))
	case *events.ClientOutdated:
		wa.UserLogin.Log.Error().Msg("Got a client outdated connect failure. The bridge is likely out of date, please update immediately.")
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: WAClientOutdated})
		wa.notifyOfflineSyncWaiter(fmt.Errorf("client outdated"))
	case *events.TemporaryBan:
		wa.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      WATemporaryBan,
			Message:    evt.String(),
		})
		wa.notifyOfflineSyncWaiter(fmt.Errorf("temporary ban: %s", evt.String()))
	default:
		log.Debug().Type("event_type", rawEvt).Msg("Unhandled WhatsApp event")
	}
	return
}

func (wa *WhatsAppClient) ensureAltJIDs(ctx context.Context, info *types.MessageSource, checkPhones bool) bool {
	var err error
	if info.Sender.Server == types.DefaultUserServer && info.SenderAlt.IsEmpty() {
		info.SenderAlt, err = wa.GetStore().LIDs.GetLIDForPN(ctx, info.Sender)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("sender", info.Sender).Msg("Failed to get LID for sender")
			return false
		}
	}
	if info.Chat.Server == types.DefaultUserServer && info.IsFromMe && info.RecipientAlt.IsEmpty() {
		info.RecipientAlt, err = wa.GetStore().LIDs.GetLIDForPN(ctx, info.Chat)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("chat", info.Chat).Msg("Failed to get LID for chat")
			return false
		}
	}
	if checkPhones {
		return wa.checkAllPhonesInMessage(ctx, info)
	}
	return true
}

func (wa *WhatsAppClient) handleWAMessage(ctx context.Context, evt *events.Message) (success bool) {
	success = true
	if evt.Info.Chat == types.StatusBroadcastJID && !wa.Main.Config.EnableStatusBroadcast {
		return
	}
	if !wa.ensureAltJIDs(ctx, &evt.Info.MessageSource, true) {
		return false
	}
	parsedMessageType := getMessageType(evt.Message)
	if encReact := evt.Message.GetEncReactionMessage(); encReact != nil {
		decrypted, err := wa.Client.DecryptReaction(ctx, evt)
		if err != nil {
			wa.UserLogin.Log.Err(err).Str("message_id", evt.Info.ID).Msg("Failed to decrypt reaction")
			return
		}
		decrypted.Key = encReact.GetTargetMessageKey()
		evt.Message.ReactionMessage = decrypted
	}
	if encComment := evt.Message.GetEncCommentMessage(); encComment != nil {
		decrypted, err := wa.Client.DecryptComment(ctx, evt)
		if err != nil {
			wa.UserLogin.Log.Err(err).Str("message_id", evt.Info.ID).Msg("Failed to decrypt comment")
		} else {
			decrypted.EncCommentMessage = evt.Message.GetEncCommentMessage()
			evt.Message = decrypted
		}
	}
	if encMessage := evt.Message.GetSecretEncryptedMessage(); encMessage != nil {
		decrypted, err := wa.Client.DecryptSecretEncryptedMessage(ctx, evt)
		if err != nil {
			wa.UserLogin.Log.Err(err).
				Str("message_id", evt.Info.ID).
				Stringer("evt_sender", evt.Info.Sender).
				Any("target_message_key", encMessage.TargetMessageKey).
				Msg("Failed to decrypt secret-encrypted message")
			return
		}
		evt.RawMessage = decrypted
		evt.UnwrapRaw()
		parsedMessageType = getMessageType(evt.Message)
	}
	wa.UserLogin.Log.Trace().
		Any("info", evt.Info).
		Any("payload", evt.Message).
		Msg("Received WhatsApp message")
	if evt.Info.IsFromMe &&
		evt.Message.GetProtocolMessage().GetHistorySyncNotification() != nil &&
		wa.Main.Bridge.Config.Backfill.Enabled {
		wa.saveWAHistorySyncNotification(ctx, evt.Message.ProtocolMessage.HistorySyncNotification)
	}
	if parsedMessageType == "ignore" || strings.HasPrefix(parsedMessageType, "unknown_protocol_") {
		return
	}

	dontRenderEdited := false
	messageAssoc := evt.Message.GetMessageContextInfo().GetMessageAssociation()
	if assocType := messageAssoc.GetAssociationType(); assocType == waE2E.MessageAssociation_HD_IMAGE_DUAL_UPLOAD || assocType == waE2E.MessageAssociation_HD_VIDEO_DUAL_UPLOAD {
		parentKey := messageAssoc.GetParentMessageKey()
		protocolMsg := evt.Message.GetProtocolMessage()
		if protocolMsg.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || protocolMsg.GetKey() == nil {
			protocolMsg = &waE2E.ProtocolMessage{
				Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           parentKey,
				EditedMessage: evt.Message.GetAssociatedChildMessage().GetMessage(),
			}
			dontRenderEdited = true
		} else if child := protocolMsg.GetEditedMessage().GetAssociatedChildMessage().GetMessage(); child != nil {
			protocolMsg.EditedMessage = child
			protocolMsg.Key = parentKey
		}
		wa.UserLogin.Log.Debug().
			Str("message_id", evt.Info.ID).
			Str("parent_id", parentKey.GetID()).
			Stringer("assoc_type", assocType).
			Msg("Received HD replacement message, converting to edit")

		evt.Message = &waE2E.Message{
			ProtocolMessage: protocolMsg,
		}
		parsedMessageType = getMessageType(evt.Message)
	} else if assocType == waE2E.MessageAssociation_MOTION_PHOTO {
		//evt.Message = evt.Message.GetAssociatedChildMessage().GetMessage()
		wa.UserLogin.Log.Debug().
			Str("message_id", evt.Info.ID).
			Str("parent_id", messageAssoc.GetParentMessageKey().GetID()).
			Msg("Ignoring motion photo update")
		return
	}

	res := wa.UserLogin.QueueRemoteEvent(&WAMessageEvent{
		MessageInfoWrapper: &MessageInfoWrapper{
			Info: evt.Info,
			wa:   wa,
		},
		Message:  evt.Message,
		MsgEvent: evt,

		parsedMessageType: parsedMessageType,
		dontRenderEdited:  dontRenderEdited,
	})
	return res.Success
}

func (wa *WhatsAppClient) handleWAUndecryptableMessage(ctx context.Context, evt *events.UndecryptableMessage) bool {
	if !wa.ensureAltJIDs(ctx, &evt.Info.MessageSource, true) {
		return false
	}
	wa.UserLogin.Log.Debug().
		Any("info", evt.Info).
		Bool("unavailable", evt.IsUnavailable).
		Str("decrypt_fail", string(evt.DecryptFailMode)).
		Msg("Received undecryptable WhatsApp message")
	wa.trackUndecryptable(evt)
	if evt.DecryptFailMode == events.DecryptFailHide {
		return true
	}
	if evt.Info.Chat == types.StatusBroadcastJID && !wa.Main.Config.EnableStatusBroadcast {
		return true
	}
	res := wa.UserLogin.QueueRemoteEvent(&WAUndecryptableMessage{
		MessageInfoWrapper: &MessageInfoWrapper{
			Info: evt.Info,
			wa:   wa,
		},
		Type: evt.UnavailableType,
	})
	return res.Success
}

func (wa *WhatsAppClient) handleWAMediaRetry(ctx context.Context, evt *events.MediaRetry) bool {
	wa.phoneSeen(evt.Timestamp)
	var senderLID, chatLID types.JID
	var err error
	if evt.SenderID.Server == types.DefaultUserServer {
		senderLID, err = wa.GetStore().LIDs.GetLIDForPN(ctx, evt.SenderID)
		if err != nil {
			wa.UserLogin.Log.Err(err).
				Stringer("sender_id", evt.SenderID).
				Msg("Failed to get LID for media retry sender")
			return false
		}
	}
	if evt.ChatID.Server == types.DefaultUserServer {
		chatLID, err = wa.GetStore().LIDs.GetLIDForPN(ctx, evt.ChatID)
		if err != nil {
			wa.UserLogin.Log.Err(err).
				Stringer("chat_id", evt.ChatID).
				Msg("Failed to get LID for media retry chat")
			return false
		}
	}
	res := wa.UserLogin.QueueRemoteEvent(&WAMediaRetry{
		MediaRetry: evt,
		wa:         wa,
		senderLID:  senderLID,
		chatLID:    chatLID,
	})
	return res.Success
}

func (wa *WhatsAppClient) handleWAReceipt(ctx context.Context, evt *events.Receipt) (success bool) {
	if evt.IsFromMe && evt.Sender.Device == 0 {
		wa.phoneSeen(evt.Timestamp)
	}
	if !wa.ensureAltJIDs(ctx, &evt.MessageSource, true) {
		return false
	}
	var evtType bridgev2.RemoteEventType
	switch evt.Type {
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		evtType = bridgev2.RemoteEventReadReceipt
	case types.ReceiptTypeDelivered:
		evtType = bridgev2.RemoteEventDeliveryReceipt
	case types.ReceiptTypeSender:
		fallthrough
	default:
		return true
	}
	targets := make([]networkid.MessageID, 0, len(evt.MessageIDs))
	messageSender := wa.GetLID()
	if !evt.MessageSender.IsEmpty() {
		messageSender = evt.MessageSender
	}
	var chatAlt types.JID
	if evt.Chat.Server == types.DefaultUserServer {
		chatLID, _ := wa.GetStore().LIDs.GetLIDForPN(ctx, evt.Chat)
		if !chatLID.IsEmpty() {
			chatAlt = evt.Chat
			evt.Chat = chatLID
		}
	}
	for _, id := range evt.MessageIDs {
		targets = append(targets, waid.MakeMessageID(evt.Chat, messageSender, id))
		if !chatAlt.IsEmpty() {
			targets = append(targets, waid.MakeMessageID(chatAlt, messageSender, id))
		}
	}
	senderLID := evt.Sender
	if senderLID.Server == types.DefaultUserServer && !evt.SenderAlt.IsEmpty() {
		senderLID = evt.SenderAlt
	}
	res := wa.UserLogin.QueueRemoteEvent(&simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      evtType,
			PortalKey: wa.makeWAPortalKey(evt.Chat),
			Sender:    wa.makeEventSender(ctx, senderLID),
			Timestamp: evt.Timestamp,
		},
		Targets: targets,
	})
	return res.Success
}

func (wa *WhatsAppClient) handleWAChatPresence(ctx context.Context, evt *events.ChatPresence) {
	if evt.Chat.Server == types.DefaultUserServer && evt.Sender.ToNonAD() == evt.Chat {
		if evt.SenderAlt.IsEmpty() {
			evt.SenderAlt, _ = wa.GetStore().LIDs.GetLIDForPN(ctx, evt.Sender)
		}
		if evt.SenderAlt.Server == types.HiddenUserServer {
			evt.Sender, evt.SenderAlt = evt.SenderAlt, evt.Sender
			evt.Chat = evt.Sender.ToNonAD()
		}
	}
	typingType := bridgev2.TypingTypeText
	timeout := 15 * time.Second
	if evt.Media == types.ChatPresenceMediaAudio {
		typingType = bridgev2.TypingTypeRecordingMedia
	}
	if evt.State == types.ChatPresencePaused {
		timeout = 0
	}

	wa.UserLogin.QueueRemoteEvent(&simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:       bridgev2.RemoteEventTyping,
			LogContext: nil,
			PortalKey:  wa.makeWAPortalKey(evt.Chat),
			Sender:     wa.makeEventSender(ctx, evt.Sender),
			Timestamp:  time.Now(),
		},
		Timeout: timeout,
		Type:    typingType,
	})
}

func (wa *WhatsAppClient) handleWALogout(reason events.ConnectFailureReason, onConnect bool) {
	errorCode := WAUnknownLogout
	if reason == events.ConnectFailureLoggedOut {
		errorCode = WALoggedOut
	} else if reason == events.ConnectFailureMainDeviceGone {
		errorCode = WAMainDeviceGone
	}
	wa.Disconnect()
	wa.Client = nil
	wa.JID = types.EmptyJID
	wa.LID = types.EmptyJID
	wa.UserLogin.Metadata.(*waid.UserLoginMetadata).WADeviceID = 0
	wa.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      errorCode,
	})
}

const callEventMaxAge = 15 * time.Minute

func (wa *WhatsAppClient) handleWACallStart(ctx context.Context, group, sender, senderAlt types.JID, id, callType string, ts time.Time) bool {
	if !wa.Main.Config.CallStartNotices || time.Since(ts) > callEventMaxAge {
		return true
	}
	if sender.Server == types.DefaultUserServer && senderAlt.IsEmpty() {
		senderAlt, _ = wa.GetStore().LIDs.GetLIDForPN(ctx, sender)
	}
	if sender.Server == types.DefaultUserServer && senderAlt.Server == types.HiddenUserServer {
		wa.UserLogin.Log.Debug().
			Stringer("lid", senderAlt).
			Stringer("pn", sender).
			Str("call_id", id).
			Msg("Forced phone number caller to LID in incoming call")
		sender, senderAlt = senderAlt, sender
	}
	chat := group
	if chat.IsEmpty() {
		chat = sender
	}
	return wa.UserLogin.QueueRemoteEvent(&simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			LogContext:   nil,
			PortalKey:    wa.makeWAPortalKey(chat),
			Sender:       wa.makeEventSender(ctx, sender),
			CreatePortal: true,
			Timestamp:    ts,
			StreamOrder:  ts.Unix(),
		},
		Data:               callType,
		ID:                 waid.MakeFakeMessageID(chat, sender, "call-"+id),
		ConvertMessageFunc: convertCallStart,
	}).Success
}

func convertCallStart(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, callType string) (*bridgev2.ConvertedMessage, error) {
	text := "Incoming call. Use the WhatsApp app to answer."
	if callType != "" {
		text = fmt.Sprintf("Incoming %s call. Use the WhatsApp app to answer.", callType)
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    text,
				BeeperActionMessage: &event.BeeperActionMessage{
					Type:     event.BeeperActionMessageCall,
					CallType: event.BeeperActionMessageCallType(callType),
				},
			},
		}},
	}, nil
}

func (wa *WhatsAppClient) handleWAIdentityChange(ctx context.Context, evt *events.IdentityChange) {
	if !wa.Main.Config.IdentityChangeNotices {
		return
	}
	if evt.JID.Server == types.DefaultUserServer {
		lid, _ := wa.GetStore().LIDs.GetLIDForPN(ctx, evt.JID)
		if !lid.IsEmpty() {
			evt.JID = lid
		}
	}
	wa.UserLogin.QueueRemoteEvent(&simplevent.Message[*events.IdentityChange]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			LogContext:   nil,
			PortalKey:    wa.makeWAPortalKey(evt.JID),
			Sender:       wa.makeEventSender(ctx, evt.JID),
			CreatePortal: false,
			Timestamp:    evt.Timestamp,
		},
		Data:               evt,
		ID:                 waid.MakeFakeMessageID(evt.JID, evt.JID, "idchange-"+strconv.FormatInt(evt.Timestamp.UnixMilli(), 10)),
		ConvertMessageFunc: convertIdentityChange,
	})
}

func convertIdentityChange(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *events.IdentityChange) (*bridgev2.ConvertedMessage, error) {
	ghost, err := portal.Bridge.GetGhostByID(ctx, waid.MakeUserID(data.JID))
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Your security code with %s changed.", ghost.Name)
	if data.Implicit {
		text = fmt.Sprintf("Your security code with %s (device #%d) changed.", ghost.Name, data.JID.Device)
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    text,
			},
		}},
	}, nil
}

func (wa *WhatsAppClient) handleWADeleteChat(ctx context.Context, evt *events.DeleteChat) bool {
	chatJID := wa.maybeConvertJIDToLID(ctx, evt.JID)
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: wa.makeWAPortalKey(chatJID),
			Timestamp: evt.Timestamp,
		},
		OnlyForMe: true,
		Children:  true,
	}).Success
}

func (wa *WhatsAppClient) handleWADeleteForMe(ctx context.Context, evt *events.DeleteForMe) bool {
	chatJID := wa.maybeConvertJIDToLID(ctx, evt.ChatJID)
	senderJID := wa.maybeConvertJIDToLID(ctx, evt.SenderJID)
	return wa.UserLogin.QueueRemoteEvent(&simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessageRemove,
			PortalKey: wa.makeWAPortalKey(chatJID),
			Timestamp: evt.Timestamp,
		},
		TargetMessage: waid.MakeMessageID(chatJID, senderJID, evt.MessageID),
		OnlyForMe:     true,
	}).Success
}

func (wa *WhatsAppClient) handleWAMarkChatAsRead(ctx context.Context, evt *events.MarkChatAsRead) bool {
	chatJID := wa.maybeConvertJIDToLID(ctx, evt.JID)
	return wa.UserLogin.QueueRemoteEvent(&simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: wa.makeWAPortalKey(chatJID),
			Sender:    wa.makeEventSender(ctx, wa.GetLID()),
			Timestamp: evt.Timestamp,
		},
		ReadUpTo: evt.Timestamp,
	}).Success
}

func (wa *WhatsAppClient) syncGhost(jid types.JID, reason string, pictureID *string) {
	log := wa.UserLogin.Log.With().
		Str("action", "sync ghost").
		Str("reason", reason).
		Str("picture_id", ptr.Val(pictureID)).
		Stringer("jid", jid).
		Logger()
	ctx := log.WithContext(wa.Main.Bridge.BackgroundCtx)
	ghost, err := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(jid))
	if err != nil {
		log.Err(err).Msg("Failed to get ghost")
		return
	}
	if pictureID != nil && *pictureID != "" && ghost.AvatarID == networkid.AvatarID(*pictureID) {
		return
	}
	userInfo, err := wa.getUserInfo(ctx, jid, pictureID != nil)
	if err != nil {
		log.Err(err).Msg("Failed to get user info")
	} else {
		ghost.UpdateInfo(ctx, userInfo)
		log.Debug().Msg("Synced ghost info")
		wa.syncAltGhostWithInfo(ctx, jid, userInfo)
	}
	go wa.syncRemoteProfile(ctx, ghost)
}

func (wa *WhatsAppClient) handleWAPictureUpdate(ctx context.Context, evt *events.Picture) bool {
	if evt.JID.Server == types.DefaultUserServer || evt.JID.Server == types.HiddenUserServer || evt.JID.Server == types.BotServer {
		go wa.syncGhost(evt.JID, "picture event", &evt.PictureID)
		return true
	} else {
		var changes bridgev2.ChatInfo
		if evt.Remove {
			changes.Avatar = &bridgev2.Avatar{Remove: true, ID: "remove"}
		} else {
			changes.ExtraUpdates = wa.makePortalAvatarFetcher(evt.PictureID, evt.Author, evt.Timestamp)
		}
		return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatInfoChange,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.
						Str("wa_event_type", "picture").
						Stringer("picture_author", evt.Author).
						Str("new_picture_id", evt.PictureID).
						Bool("remove_picture", evt.Remove)
				},
				PortalKey: wa.makeWAPortalKey(evt.JID),
				Sender:    wa.makeEventSender(ctx, evt.Author),
				Timestamp: evt.Timestamp,
			},
			ChatInfoChange: &bridgev2.ChatInfoChange{
				ChatInfo: &changes,
			},
		}).Success
	}
}

func (wa *WhatsAppClient) handleWAGroupInfoChange(ctx context.Context, evt *events.GroupInfo) bool {
	eventMeta := simplevent.EventMeta{
		Type:         bridgev2.RemoteEventChatInfoChange,
		LogContext:   nil,
		PortalKey:    wa.makeWAPortalKey(evt.JID),
		CreatePortal: true,
		Timestamp:    evt.Timestamp,
	}
	if evt.Sender != nil {
		eventMeta.Sender = wa.makeEventSender(ctx, *evt.Sender)
	}
	if evt.Delete != nil {
		eventMeta.Type = bridgev2.RemoteEventChatDelete
		return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatDelete{EventMeta: eventMeta}).Success
	} else {
		return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
			EventMeta:      eventMeta,
			ChatInfoChange: wa.wrapGroupInfoChange(ctx, evt),
		}).Success
	}
}

func (wa *WhatsAppClient) handleWAJoinedGroup(ctx context.Context, evt *events.JoinedGroup) bool {
	if wa.createDedup.Pop(evt.CreateKey) {
		return true
	}
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			LogContext:   nil,
			PortalKey:    wa.makeWAPortalKey(evt.JID),
			CreatePortal: true,
		},
		ChatInfo: wa.wrapGroupInfo(ctx, &evt.GroupInfo),
	}).Success
}

func (wa *WhatsAppClient) handleWANewsletterJoin(ctx context.Context, evt *events.NewsletterJoin) bool {
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			LogContext:   nil,
			PortalKey:    wa.makeWAPortalKey(evt.ID),
			CreatePortal: true,
		},
		ChatInfo: wa.wrapNewsletterInfo(ctx, &evt.NewsletterMetadata),
	}).Success
}

func (wa *WhatsAppClient) handleWANewsletterLeave(evt *events.NewsletterLeave) bool {
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:       bridgev2.RemoteEventChatDelete,
			LogContext: nil,
			PortalKey:  wa.makeWAPortalKey(evt.ID),
		},
		OnlyForMe: true,
	}).Success
}

func (wa *WhatsAppClient) handleWAUserLocalPortalInfo(chatJID types.JID, ts time.Time, info *bridgev2.UserLocalPortalInfo) bool {
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: wa.makeWAPortalKey(chatJID),
			Timestamp: ts,
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				UserLocal: info,
			},
		},
	}).Success
}

func (wa *WhatsAppClient) handleWAMute(evt *events.Mute) bool {
	var mutedUntil time.Time
	if evt.Action.GetMuted() {
		mutedUntil = event.MutedForever
		if evt.Action.GetMuteEndTimestamp() > 0 {
			mutedUntil = time.Unix(evt.Action.GetMuteEndTimestamp(), 0)
		}
	} else {
		mutedUntil = bridgev2.Unmuted
	}
	return wa.handleWAUserLocalPortalInfo(evt.JID, evt.Timestamp, &bridgev2.UserLocalPortalInfo{
		MutedUntil: &mutedUntil,
	})
}

func (wa *WhatsAppClient) handleWAArchive(evt *events.Archive) bool {
	var tag event.RoomTag
	if evt.Action.GetArchived() {
		tag = wa.Main.Config.ArchiveTag
	}
	return wa.handleWAUserLocalPortalInfo(evt.JID, evt.Timestamp, &bridgev2.UserLocalPortalInfo{
		Tag: &tag,
	})
}

func (wa *WhatsAppClient) handleWAPin(evt *events.Pin) bool {
	var tag event.RoomTag
	if evt.Action.GetPinned() {
		tag = wa.Main.Config.PinnedTag
	}
	return wa.handleWAUserLocalPortalInfo(evt.JID, evt.Timestamp, &bridgev2.UserLocalPortalInfo{
		Tag: &tag,
	})
}

func (wa *WhatsAppClient) handleWAAppStateSyncComplete(ctx context.Context, evt *events.AppStateSyncComplete) {
	log := zerolog.Ctx(ctx).With().
		Str("patch_name", string(evt.Name)).
		Uint64("patch_version", evt.Version).
		Logger()
	if len(wa.GetStore().PushName) > 0 && evt.Name == appstate.WAPatchCriticalBlock {
		err := wa.updatePresence(ctx, types.PresenceUnavailable)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to send presence after app state sync")
		}
		go wa.syncRemoteProfile(log.WithContext(context.Background()), nil)
	} else if evt.Name == appstate.WAPatchCriticalUnblockLow {
		go wa.resyncContacts(false, true)
	}
	wa.appStateRecoveryLock.Lock()
	defer wa.appStateRecoveryLock.Unlock()
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	if ts, exists := meta.AppStateRecoveryAttempted[evt.Name]; exists {
		delete(wa.appStateFullSyncAttempted, evt.Name)
		delete(meta.AppStateRecoveryAttempted, evt.Name)
		err := wa.UserLogin.Save(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save login metadata after unmarking app state recovery as attempted")
		} else {
			log.Info().
				Time("recovery_ts", ts).
				Bool("recovery_evt", evt.Recovery).
				Msg("Unmarked app state recovery as attempted after successful full sync")
			wa.UserLogin.TrackAnalytics("WhatsApp Appstate Recovery Success", map[string]any{
				"patch_name":    evt.Name,
				"from_recovery": evt.Recovery,
			})
		}
	} else if ts, exists = wa.appStateFullSyncAttempted[evt.Name]; exists {
		delete(wa.appStateFullSyncAttempted, evt.Name)
		log.Debug().Time("full_sync_ts", ts).Msg("Unmarked app state full sync attempted after successful sync")
	}
}

func (wa *WhatsAppClient) handleWAAppStateSyncError(ctx context.Context, evt *events.AppStateSyncError) {
	log := zerolog.Ctx(ctx).With().
		Str("patch_name", string(evt.Name)).
		Logger()
	wa.appStateRecoveryLock.Lock()
	defer wa.appStateRecoveryLock.Unlock()
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	lastRecovery := meta.AppStateRecoveryAttempted[evt.Name]
	lastFullSync := wa.appStateFullSyncAttempted[evt.Name]
	if !lastRecovery.IsZero() && time.Since(lastRecovery) < 48*time.Hour {
		log.Debug().Err(evt.Error).
			Time("last_recovery_attempt", lastRecovery).
			Time("last_full_sync_attempt", lastFullSync).
			Msg("App state sync failed, but recovery already attempted")
		return
	}
	if !evt.FullSync {
		if !lastFullSync.IsZero() {
			log.Debug().
				Err(evt.Error).
				Time("last_full_sync_attempt", lastFullSync).
				Msg("App state sync failed, but full sync already attempted")
			return
		}
		wa.appStateFullSyncAttempted[evt.Name] = time.Now()
		log.Info().
			Err(evt.Error).
			Msg("Trying full sync for app state after partial sync error")
		go func() {
			err := wa.Client.FetchAppState(ctx, evt.Name, true, false)
			if err != nil {
				log.Err(err).Msg("Full app state sync failed")
			} else {
				log.Debug().Msg("Full app state sync succeeded")
			}
		}()
		return
	}
	log.Info().
		Err(evt.Error).
		Msg("Trying recovery for app state after full sync error")
	if meta.AppStateRecoveryAttempted == nil {
		meta.AppStateRecoveryAttempted = make(map[appstate.WAPatchName]time.Time)
	}
	meta.AppStateRecoveryAttempted[evt.Name] = time.Now()
	err := wa.UserLogin.Save(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to save login metadata after marking app state recovery as attempted")
	}
	wa.UserLogin.TrackAnalytics("WhatsApp Appstate Recovery Request", map[string]any{
		"patch_name": evt.Name,
	})
	go func() {
		resp, err := wa.Client.SendPeerMessage(ctx, whatsmeow.BuildAppStateRecoveryRequest(evt.Name))
		if err != nil {
			log.Err(err).Msg("Failed to send app state recovery request")
		} else {
			log.Debug().
				Str("message_id", resp.ID).
				Time("message_ts", resp.Timestamp).
				Msg("Sent app state recovery request")
		}
	}()
}
