package connector

import (
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) makeWAPortalKey(chatJID types.JID) networkid.PortalKey {
	key := networkid.PortalKey{
		ID: waid.MakePortalID(chatJID),
	}
	switch chatJID.Server {
	case types.DefaultUserServer, types.BotServer, types.HiddenUserServer, types.BroadcastServer:
		key.Receiver = wa.UserLogin.ID
	default:
		if wa.Main.Bridge.Config.SplitPortals {
			key.Receiver = wa.UserLogin.ID
		}
	}
	return key
}

func (wa *WhatsAppClient) makeEventSender(id types.JID) bridgev2.EventSender {
	if id.Server == types.NewsletterServer {
		// Send as bot
		return bridgev2.EventSender{}
	}
	return bridgev2.EventSender{
		IsFromMe:    id.User == wa.GetStore().GetJID().User || id.User == wa.GetStore().GetLID().User,
		Sender:      waid.MakeUserID(id),
		SenderLogin: waid.MakeUserLoginID(id),
	}
}

func (wa *WhatsAppClient) messageIDToKey(id *waid.ParsedMessageID) *waCommon.MessageKey {
	key := &waCommon.MessageKey{
		RemoteJID: ptr.Ptr(id.Chat.String()),
		ID:        ptr.Ptr(id.ID),
	}
	if id.Sender.User == wa.GetStore().GetJID().User || id.Sender.User == wa.GetStore().GetLID().User {
		key.FromMe = ptr.Ptr(true)
	}
	if id.Chat.Server != types.MessengerServer && id.Chat.Server != types.DefaultUserServer && id.Chat.Server != types.HiddenUserServer && id.Chat.Server != types.BotServer {
		key.Participant = ptr.Ptr(id.Sender.String())
	}
	return key
}
