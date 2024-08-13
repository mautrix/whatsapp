package connector

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

type WAMessageEvent struct {
	*events.Message
	portalKey networkid.PortalKey
	wa        *WhatsAppClient
}

var (
	_ bridgev2.RemoteMessage            = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventWithTimestamp = (*WAMessageEvent)(nil)
)

func (evt *WAMessageEvent) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventMessage
}

func (evt *WAMessageEvent) GetPortalKey() networkid.PortalKey {
	return evt.portalKey
}

func (evt *WAMessageEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.Str("message_id", evt.Info.ID).Uint64("sender_id", evt.Info.Sender.UserInt())
}

func (evt *WAMessageEvent) GetSender() bridgev2.EventSender {
	return evt.wa.makeEventSender(int64(evt.Info.Sender.UserInt()))
}

func (evt *WAMessageEvent) GetID() networkid.MessageID {
	return waid.MakeMessageID(evt.Info.Chat, evt.Info.Sender, evt.Info.ID)
}

func (evt *WAMessageEvent) GetTimestamp() time.Time {
	return evt.GetTimestamp()
}

func (evt *WAMessageEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	return evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, evt.Message), nil
}
