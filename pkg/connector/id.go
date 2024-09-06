package connector

import (
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) makeWAPortalKey(chatJID types.JID) (key networkid.PortalKey) {
	key.ID = waid.MakePortalID(chatJID)
	switch chatJID.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.BroadcastServer:
		key.Receiver = wa.UserLogin.ID
	}
	return
}

func (wa *WhatsAppClient) makeEventSender(id types.JID) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    waid.MakeUserLoginID(id) == wa.UserLogin.ID,
		Sender:      waid.MakeUserID(id),
		SenderLogin: waid.MakeUserLoginID(id),
	}
}

func (wa *WhatsAppClient) messageIDToKey(id *waid.ParsedMessageID) *waCommon.MessageKey {
	key := &waCommon.MessageKey{
		RemoteJID: ptr.Ptr(id.Chat.String()),
		ID:        ptr.Ptr(id.ID),
	}
	if id.Sender.User == string(wa.UserLogin.ID) {
		key.FromMe = ptr.Ptr(true)
	}
	if id.Chat.Server != types.MessengerServer && id.Chat.Server != types.DefaultUserServer {
		key.Participant = ptr.Ptr(id.Sender.String())
	}
	return key
}

func (wa *WhatsAppClient) keyToMessageID(chat, sender types.JID, key *waCommon.MessageKey) networkid.MessageID {
	sender = sender.ToNonAD()
	var err error
	if !key.GetFromMe() {
		if key.GetParticipant() != "" {
			sender, err = types.ParseJID(key.GetParticipant())
			if err != nil {
				// TODO log somehow?
				return ""
			}
			if sender.Server == types.LegacyUserServer {
				sender.Server = types.DefaultUserServer
			}
		} else if chat.Server == types.DefaultUserServer {
			ownID := ptr.Val(wa.Device.ID).ToNonAD()
			if sender.User == ownID.User {
				sender = chat
			} else {
				sender = ownID
			}
		} else {
			// TODO log somehow?
			return ""
		}
	}
	remoteJID, err := types.ParseJID(key.GetRemoteJID())
	if err == nil && !remoteJID.IsEmpty() {
		// TODO use remote jid in other cases?
		if remoteJID.Server == types.GroupServer {
			chat = remoteJID
		}
	}
	return waid.MakeMessageID(chat, sender, key.GetID())
}
