package connector

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

const (
	WADisconnected   status.BridgeStateErrorCode = "wa-transient-disconnect"
	WAPermanentError status.BridgeStateErrorCode = "wa-unknown-permanent-error"
)

func (wa *WhatsAppClient) handleWAEvent(rawEvt any) {
	log := wa.UserLogin.Log

	switch evt := rawEvt.(type) {
	case events.Message:
		log.Trace().
			Any("info", evt.Info).
			Any("payload", evt.Message).
			Msg("Received WhatsApp message")
		portalKey, ok := wa.makeWAPortalKey(evt.Info.Chat)
		if !ok {
			log.Warn().Stringer("chat", evt.Info.Chat).Msg("Ignoring WhatsApp message with unknown chat JID")
			return
		}
		wa.Main.Bridge.QueueRemoteEvent(wa.UserLogin, &WAMessageEvent{
			Message:   &evt,
			portalKey: portalKey,
			wa:        wa,
		})
	case *events.Receipt:
		portalKey, ok := wa.makeWAPortalKey(evt.Chat)
		if !ok {
			log.Warn().Stringer("chat", evt.Chat).Msg("Ignoring WhatsApp receipt with unknown chat JID")
			return
		}
		var evtType bridgev2.RemoteEventType
		switch evt.Type {
		case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
			evtType = bridgev2.RemoteEventReadReceipt
		case types.ReceiptTypeDelivered:
			evtType = bridgev2.RemoteEventDeliveryReceipt
		}
		targets := make([]networkid.MessageID, len(evt.MessageIDs))
		for i, id := range evt.MessageIDs {
			targets[i] = waid.MakeMessageID(evt.Chat, *wa.Device.ID, id)
		}
		wa.Main.Bridge.QueueRemoteEvent(wa.UserLogin, &simplevent.Receipt{
			EventMeta: simplevent.EventMeta{
				Type:       evtType,
				LogContext: nil,
				PortalKey:  portalKey,
				Sender:     wa.makeWAEventSender(evt.Sender),
				Timestamp:  evt.Timestamp,
			},
			Targets: targets,
		})
	case *events.Connected:
		log.Debug().Msg("Connected to WhatsApp socket")
		wa.State = status.BridgeState{StateEvent: status.StateConnected}
		wa.UserLogin.BridgeState.Send(wa.State)
	case *events.Disconnected:
		log.Debug().Msg("Disconnected from WhatsApp socket")
		wa.State = status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      WADisconnected,
		}
		wa.UserLogin.BridgeState.Send(wa.State)
	case events.PermanentDisconnect:
		switch e := evt.(type) {
		case *events.LoggedOut:
			if e.Reason == events.ConnectFailureLoggedOut && !e.OnConnect && wa.canReconnect() {
				wa.resetWADevice()
				log.Debug().Msg("Doing full reconnect after WhatsApp 401 error")
				go wa.FullReconnect()
			}
		case *events.ConnectFailure:
			if e.Reason == events.ConnectFailureNotFound {
				if wa.Client != nil {
					wa.Client.Disconnect()
					err := wa.Device.Delete()
					if err != nil {
						log.Error().Msg(fmt.Sprintf("Error deleting device %s", err.Error()))
						return
					}
					wa.resetWADevice()
					wa.Client = nil
				}
				log.Debug().Msg("Reconnecting e2ee client after WhatsApp 415 error")
				go func() {
					err := wa.Connect(context.Background())
					if err != nil {
						log.Error().Msg(fmt.Sprintf("Error connecting %s", err.Error()))
						return
					}
				}()
			}
		}

		wa.State = status.BridgeState{
			StateEvent: status.StateUnknownError,
			Error:      WAPermanentError,
			Message:    evt.PermanentDisconnectDescription(),
		}
		wa.UserLogin.BridgeState.Send(wa.State)
	default:
		log.Debug().Type("event_type", rawEvt).Msg("Unhandled WhatsApp event")
	}
}
