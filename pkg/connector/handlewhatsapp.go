package connector

import (
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

const (
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

func (wa *WhatsAppClient) handleWAEvent(rawEvt any) {
	log := wa.UserLogin.Log

	switch evt := rawEvt.(type) {
	case *events.Message:
		log.Trace().
			Any("info", evt.Info).
			Any("payload", evt.Message).
			Msg("Received WhatsApp message")
		if evt.Info.Chat.Server == types.HiddenUserServer || evt.Info.Sender.Server == types.HiddenUserServer {
			return
		}
		wa.Main.Bridge.QueueRemoteEvent(wa.UserLogin, &WAMessageEvent{
			Message: evt,
			wa:      wa,
		})
	case *events.Receipt:
		var evtType bridgev2.RemoteEventType
		switch evt.Type {
		case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
			evtType = bridgev2.RemoteEventReadReceipt
		case types.ReceiptTypeDelivered:
			evtType = bridgev2.RemoteEventDeliveryReceipt
		case types.ReceiptTypeSender:
			fallthrough
		default:
			return
		}
		targets := make([]networkid.MessageID, len(evt.MessageIDs))
		for i, id := range evt.MessageIDs {
			targets[i] = waid.MakeMessageID(evt.Chat, *wa.Device.ID, id)
		}
		wa.Main.Bridge.QueueRemoteEvent(wa.UserLogin, &simplevent.Receipt{
			EventMeta: simplevent.EventMeta{
				Type:      evtType,
				PortalKey: wa.makeWAPortalKey(evt.Chat),
				Sender:    wa.makeEventSender(evt.Sender),
				Timestamp: evt.Timestamp,
			},
			Targets: targets,
		})
	case *events.ChatPresence:
		typingType := bridgev2.TypingTypeText
		timeout := 15 * time.Second
		if evt.Media == types.ChatPresenceMediaAudio {
			typingType = bridgev2.TypingTypeRecordingMedia
		}
		if evt.State == types.ChatPresencePaused {
			timeout = 0
		}

		wa.Main.Bridge.QueueRemoteEvent(wa.UserLogin, &simplevent.Typing{
			EventMeta: simplevent.EventMeta{
				Type:       bridgev2.RemoteEventTyping,
				LogContext: nil,
				PortalKey:  wa.makeWAPortalKey(evt.Chat),
				Sender:     wa.makeEventSender(evt.Sender),
				Timestamp:  time.Now(),
			},
			Timeout: timeout,
			Type:    typingType,
		})
	case *events.Connected:
		log.Debug().Msg("Connected to WhatsApp socket")
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	case *events.Disconnected:
		// Don't send the normal transient disconnect state if we're already in a different transient disconnect state.
		// TODO remove this if/when the phone offline state is moved to a sub-state of CONNECTED
		if wa.UserLogin.BridgeState.GetPrev().Error != WAPhoneOffline && wa.PhoneRecentlySeen(false) {
			wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateTransientDisconnect, Error: WADisconnected})
		}
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
	case *events.StreamReplaced:
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: WAStreamReplaced})
	case *events.ConnectFailure:
		wa.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      status.BridgeStateErrorCode(fmt.Sprintf("wa-connect-failure-%d", evt.Reason)),
			Message:    fmt.Sprintf("Unknown connection failure: %s (%s)", evt.Reason, evt.Message),
		})
	case *events.ClientOutdated:
		wa.UserLogin.Log.Error().Msg("Got a client outdated connect failure. The bridge is likely out of date, please update immediately.")
		wa.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateUnknownError, Error: WAClientOutdated})
	case *events.TemporaryBan:
		wa.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      WATemporaryBan,
			Message:    evt.String(),
		})
	default:
		log.Debug().Type("event_type", rawEvt).Msg("Unhandled WhatsApp event")
	}
}
