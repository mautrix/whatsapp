package connector

import (
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) makeWAPortalKey(chatJID types.JID) (key networkid.PortalKey, ok bool) {
	key.ID = waid.MakeWAPortalID(chatJID)
	switch chatJID.Server {
	case types.DefaultUserServer, types.GroupServer: //TODO: LID support + other types?
		key.Receiver = wa.UserLogin.ID // does this also apply for groups ?!?!
	default:
		return
	}
	ok = true
	return
}

func (wa *WhatsAppClient) makeWAEventSender(sender types.JID) bridgev2.EventSender {
	return wa.makeEventSender(int64(sender.UserInt()))
}

func (wa *WhatsAppClient) makeEventSender(id int64) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    waid.MakeUserLoginID(id) == wa.UserLogin.ID,
		Sender:      waid.MakeUserID(id),
		SenderLogin: waid.MakeUserLoginID(id),
	}
}
