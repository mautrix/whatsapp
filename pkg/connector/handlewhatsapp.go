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

	success = true
	switch evt := rawEvt.(type) {
	case *events.Message:
		success = wa.handleWAMessage(ctx, evt)
	case *events.Receipt:
		success = wa.handleWAReceipt(ctx, evt)
	case *events.ChatPresence:
		wa.handleWAChatPresence(ctx, evt)
	case *events.UndecryptableMessage:
		success = wa.handleWAUndecryptableMessage(evt)

	case *events.CallOffer:
		success = wa.handleWACallStart(ctx, evt.GroupJID, evt.CallCreator, evt.CallID, "", evt.Timestamp)
	case *events.CallOfferNotice:
		success = wa.handleWACallStart(ctx, evt.GroupJID, evt.CallCreator, evt.CallID, evt.Type, evt.Timestamp)
	case *events.CallTerminate, *events.CallRelayLatency, *events.CallAccept, *events.UnknownCallEvent:
		// ignore
	case *events.IdentityChange:
		wa.handleWAIdentityChange(ctx, evt)
	case *events.MarkChatAsRead:
		success = wa.handleWAMarkChatAsRead(ctx, evt)
	case *events.DeleteForMe:
		success = wa.handleWADeleteForMe(evt)
	case *events.DeleteChat:
		success = wa.handleWADeleteChat(evt)
	case *events.Mute:
		success = wa.handleWAMute(evt)
	case *events.Archive:
		success = wa.handleWAArchive(evt)
	case *events.Pin:
		success = wa.handleWAPin(evt)

	case *events.HistorySync:
		if wa.Main.Bridge.Config.Backfill.Enabled {
			wa.historySyncs <- evt.Data
		}
	case *events.MediaRetry:
		wa.phoneSeen(evt.Timestamp)
		success = wa.UserLogin.QueueRemoteEvent(&WAMediaRetry{MediaRetry: evt, wa: wa}).Success

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

	case *events.AppStateSyncComplete:
		if len(wa.GetStore().PushName) > 0 && evt.Name == appstate.WAPatchCriticalBlock {
			err := wa.updatePresence(types.PresenceUnavailable)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to send presence after app state sync")
			}
			go wa.syncRemoteProfile(log.WithContext(context.Background()), nil)
		} else if evt.Name == appstate.WAPatchCriticalUnblockLow {
			go wa.resyncContacts(false, true)
		}
	case *events.AppState:
		// Intentionally ignored
	case *events.PushNameSetting:
		// Send presence available when connecting and when the pushname is changed.
		// This makes sure that outgoing messages always have the right pushname.
		err := wa.updatePresence(types.PresenceUnavailable)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to send presence after push name update")
		}
		_, _, err = wa.GetStore().Contacts.PutPushName(ctx, wa.JID.ToNonAD(), evt.Action.GetName())
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
				err := wa.updatePresence(types.PresenceUnavailable)
				if err != nil {
					log.Warn().Err(err).Msg("Failed to send initial presence after connecting")
				}
			}()
			go wa.syncRemoteProfile(log.WithContext(context.Background()), nil)
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
				Time("phone_last_seen", wa.UserLogin.Metadata.(*waid.UserLoginMetadata).PhoneLastSeen.Time).
				Msg("Offline sync completed, but phone last seen date is still old")
		} else {
			log.Info().Msg("Offline sync completed")
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

func (wa *WhatsAppClient) handleWAMessage(ctx context.Context, evt *events.Message) (success bool) {
	success = true
	if evt.Info.Chat.Server == types.HiddenUserServer && evt.Info.Sender.ToNonAD() == evt.Info.Chat && evt.Info.SenderAlt.Server == types.DefaultUserServer {
		wa.UserLogin.Log.Debug().
			Stringer("lid", evt.Info.Sender).
			Stringer("pn", evt.Info.SenderAlt).
			Str("message_id", evt.Info.ID).
			Msg("Forced LID DM sender to phone number in incoming message")
		evt.Info.Sender, evt.Info.SenderAlt = evt.Info.SenderAlt, evt.Info.Sender
		evt.Info.Chat = evt.Info.Sender.ToNonAD()
	} else if evt.Info.Chat.Server == types.HiddenUserServer && evt.Info.IsFromMe && evt.Info.RecipientAlt.Server == types.DefaultUserServer {
		wa.UserLogin.Log.Debug().
			Stringer("lid", evt.Info.Chat).
			Stringer("pn", evt.Info.RecipientAlt).
			Str("message_id", evt.Info.ID).
			Msg("Forced LID DM sender to phone number in own message sent from another device")
		evt.Info.Chat = evt.Info.RecipientAlt.ToNonAD()
	} else if evt.Info.Sender.Server == types.BotServer && evt.Info.Chat.Server == types.HiddenUserServer {
		chatPN, err := wa.Device.LIDs.GetPNForLID(ctx, evt.Info.Chat)
		if err != nil {
			wa.UserLogin.Log.Err(err).
				Str("message_id", evt.Info.ID).
				Stringer("lid", evt.Info.Chat).
				Msg("Failed to get phone number of DM for incoming bot message")
		} else if !chatPN.IsEmpty() {
			wa.UserLogin.Log.Debug().
				Stringer("lid", evt.Info.Chat).
				Stringer("pn", chatPN).
				Str("message_id", evt.Info.ID).
				Msg("Forced LID chat to phone number in bot message")
			evt.Info.Chat = chatPN
		}
	}
	wa.UserLogin.Log.Trace().
		Any("info", evt.Info).
		Any("payload", evt.Message).
		Msg("Received WhatsApp message")
	if evt.Info.Chat == types.StatusBroadcastJID && !wa.Main.Config.EnableStatusBroadcast {
		return
	}
	if evt.Info.IsFromMe &&
		evt.Message.GetProtocolMessage().GetHistorySyncNotification() != nil &&
		wa.Main.Bridge.Config.Backfill.Enabled &&
		wa.Client.ManualHistorySyncDownload {
		wa.saveWAHistorySyncNotification(ctx, evt.Message.ProtocolMessage.HistorySyncNotification)
	}

	messageAssoc := evt.Message.GetMessageContextInfo().GetMessageAssociation()
	if assocType := messageAssoc.GetAssociationType(); assocType == waE2E.MessageAssociation_HD_IMAGE_DUAL_UPLOAD || assocType == waE2E.MessageAssociation_HD_VIDEO_DUAL_UPLOAD {
		parentKey := messageAssoc.GetParentMessageKey()
		associatedMessage := evt.Message.GetAssociatedChildMessage().GetMessage()
		wa.UserLogin.Log.Debug().
			Str("message_id", evt.Info.ID).
			Str("parent_id", parentKey.GetID()).
			Stringer("assoc_type", assocType).
			Msg("Received HD replacement message, converting to edit")

		protocolMsg := &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:           parentKey,
			EditedMessage: associatedMessage,
		}
		evt.Message = &waE2E.Message{
			ProtocolMessage: protocolMsg,
		}
	}

	parsedMessageType := getMessageType(evt.Message)
	if parsedMessageType == "ignore" || strings.HasPrefix(parsedMessageType, "unknown_protocol_") {
		return
	}
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
	res := wa.UserLogin.QueueRemoteEvent(&WAMessageEvent{
		MessageInfoWrapper: &MessageInfoWrapper{
			Info: evt.Info,
			wa:   wa,
		},
		Message:  evt.Message,
		MsgEvent: evt,

		parsedMessageType: parsedMessageType,
	})
	return res.Success
}

func (wa *WhatsAppClient) handleWAUndecryptableMessage(evt *events.UndecryptableMessage) bool {
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

func (wa *WhatsAppClient) handleWAReceipt(ctx context.Context, evt *events.Receipt) (success bool) {
	if evt.Chat.Server == types.HiddenUserServer && evt.Sender.ToNonAD() == evt.Chat && evt.SenderAlt.Server == types.DefaultUserServer {
		wa.UserLogin.Log.Debug().
			Stringer("lid", evt.Sender).
			Stringer("pn", evt.SenderAlt).
			Strs("message_id", evt.MessageIDs).
			Msg("Forced LID DM sender to phone number in incoming receipt")
		evt.Sender, evt.SenderAlt = evt.SenderAlt, evt.Sender
		evt.Chat = evt.Sender.ToNonAD()
	} else if evt.Chat.Server == types.HiddenUserServer && evt.IsFromMe && evt.RecipientAlt.Server == types.DefaultUserServer {
		wa.UserLogin.Log.Debug().
			Stringer("lid", evt.Chat).
			Stringer("pn", evt.RecipientAlt).
			Strs("message_id", evt.MessageIDs).
			Msg("Forced LID DM sender to phone number in own receipt sent from another device")
		evt.Chat = evt.RecipientAlt.ToNonAD()
	}
	if evt.IsFromMe && evt.Sender.Device == 0 {
		wa.phoneSeen(evt.Timestamp)
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
	targets := make([]networkid.MessageID, len(evt.MessageIDs))
	messageSender := wa.JID
	if !evt.MessageSender.IsEmpty() {
		messageSender = evt.MessageSender
	} else if evt.Chat.Server == types.GroupServer && evt.Sender.Server == types.HiddenUserServer {
		lid := wa.Device.GetLID()
		if !lid.IsEmpty() {
			messageSender = lid
		}
	}
	for i, id := range evt.MessageIDs {
		targets[i] = waid.MakeMessageID(evt.Chat, messageSender, id)
	}
	res := wa.UserLogin.QueueRemoteEvent(&simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      evtType,
			PortalKey: wa.makeWAPortalKey(evt.Chat),
			Sender:    wa.makeEventSender(ctx, evt.Sender),
			Timestamp: evt.Timestamp,
		},
		Targets: targets,
	})
	return res.Success
}

func (wa *WhatsAppClient) handleWAChatPresence(ctx context.Context, evt *events.ChatPresence) {
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
	wa.Client.Disconnect()
	wa.Client = nil
	wa.JID = types.EmptyJID
	wa.UserLogin.Metadata.(*waid.UserLoginMetadata).WADeviceID = 0
	wa.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      errorCode,
	})
}

const callEventMaxAge = 15 * time.Minute

func (wa *WhatsAppClient) handleWACallStart(ctx context.Context, group, sender types.JID, id, callType string, ts time.Time) bool {
	if !wa.Main.Config.CallStartNotices || time.Since(ts) > callEventMaxAge {
		return true
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
			},
		}},
	}, nil
}

func (wa *WhatsAppClient) handleWAIdentityChange(ctx context.Context, evt *events.IdentityChange) {
	if !wa.Main.Config.IdentityChangeNotices {
		return
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

func (wa *WhatsAppClient) handleWADeleteChat(evt *events.DeleteChat) bool {
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: wa.makeWAPortalKey(evt.JID),
			Timestamp: evt.Timestamp,
		},
		OnlyForMe: true,
	}).Success
}

func (wa *WhatsAppClient) handleWADeleteForMe(evt *events.DeleteForMe) bool {
	return wa.UserLogin.QueueRemoteEvent(&simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessageRemove,
			PortalKey: wa.makeWAPortalKey(evt.ChatJID),
			Timestamp: evt.Timestamp,
		},
		TargetMessage: waid.MakeMessageID(evt.ChatJID, evt.SenderJID, evt.MessageID),
		OnlyForMe:     true,
	}).Success
}

func (wa *WhatsAppClient) handleWAMarkChatAsRead(ctx context.Context, evt *events.MarkChatAsRead) bool {
	return wa.UserLogin.QueueRemoteEvent(&simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: wa.makeWAPortalKey(evt.JID),
			Sender:    wa.makeEventSender(ctx, wa.JID),
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
		if evt.Action.GetMuteEndTimestamp() != 0 {
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
