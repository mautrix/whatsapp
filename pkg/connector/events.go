package connector

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

type WAMessageEvent struct {
	*events.Message
	wa *WhatsAppClient
}

var (
	_ bridgev2.RemoteMessage                  = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageWithTransactionID = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventWithTimestamp       = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReaction                 = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReactionRemove           = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEdit                     = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageRemove            = (*WAMessageEvent)(nil)
)

func (evt *WAMessageEvent) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	if len(existing) > 1 {
		zerolog.Ctx(ctx).Warn().Msg("Got edit to message with multiple parts")
	}
	editedMsg := evt.Message.Message.GetProtocolMessage().GetEditedMessage()

	// TODO edits to media captions may not contain the media
	cm := evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, editedMsg)
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{cm.Parts[0].ToEditPart(existing[0])},
	}, nil
}

func (evt *WAMessageEvent) GetTargetMessage() networkid.MessageID {
	if reactionMsg := evt.Message.Message.GetReactionMessage(); reactionMsg != nil {
		key := reactionMsg.GetKey()
		senderID := key.GetParticipant()
		if senderID == "" {
			if key.GetFromMe() == true {
				senderID = evt.Info.Sender.ToNonAD().String()
			} else {
				senderID = evt.wa.Client.Store.ID.ToNonAD().String() // could be false in groups
			}
		}
		senderJID, _ := types.ParseJID(senderID)
		return waid.MakeMessageID(evt.Info.Chat, senderJID, *key.ID)
	} else if protocolMsg := evt.Message.Message.GetProtocolMessage(); protocolMsg != nil {
		key := protocolMsg.GetKey()
		senderID := key.GetParticipant()
		if senderID == "" {
			if key.GetFromMe() == true {
				senderID = evt.Info.Sender.ToNonAD().String()
			} else {
				senderID = evt.wa.Client.Store.ID.ToNonAD().String() // could be false in groups
			}
		}
		senderJID, _ := types.ParseJID(senderID)
		return waid.MakeMessageID(evt.Info.Chat, senderJID, *key.ID)
	}
	return ""
}

func (evt *WAMessageEvent) GetReactionEmoji() (string, networkid.EmojiID) {
	return evt.Message.Message.GetReactionMessage().GetText(), ""
}

func (evt *WAMessageEvent) GetRemovedEmojiID() networkid.EmojiID {
	return ""
}

func (evt *WAMessageEvent) ShouldCreatePortal() bool {
	return true
}

func (evt *WAMessageEvent) GetType() bridgev2.RemoteEventType {
	waMsg := evt.Message.Message
	if waMsg.ReactionMessage != nil {
		if waMsg.ReactionMessage.GetText() == "" {
			return bridgev2.RemoteEventReactionRemove
		}
		return bridgev2.RemoteEventReaction
	} else if waMsg.ProtocolMessage != nil && waMsg.ProtocolMessage.Type != nil {
		switch *waMsg.ProtocolMessage.Type {
		case waE2E.ProtocolMessage_REVOKE:
			return bridgev2.RemoteEventMessageRemove
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			return bridgev2.RemoteEventEdit
		}
	}
	return bridgev2.RemoteEventMessage
}

func (evt *WAMessageEvent) GetPortalKey() networkid.PortalKey {
	return evt.wa.makeWAPortalKey(evt.Info.Chat)
}

func (evt *WAMessageEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.Str("message_id", evt.Info.ID).Stringer("sender_id", evt.Info.Sender)
}

func (evt *WAMessageEvent) GetTimestamp() time.Time {
	return evt.Info.Timestamp
}

func (evt *WAMessageEvent) GetSender() bridgev2.EventSender {
	return evt.wa.makeEventSender(evt.Info.Sender)
}

func (evt *WAMessageEvent) GetID() networkid.MessageID {
	return waid.MakeMessageID(evt.Info.Chat, evt.Info.Sender, evt.Info.ID)
}

func (evt *WAMessageEvent) GetTransactionID() networkid.TransactionID {
	// TODO for newsletter messages, there's a different transaction ID
	return networkid.TransactionID(evt.GetID())
}

func (evt *WAMessageEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	return evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, evt.Message.Message), nil
}
