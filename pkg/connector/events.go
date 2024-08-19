package connector

import (
	"context"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

type WAMessageEvent struct {
	*events.Message
	portalKey networkid.PortalKey
	wa        *WhatsAppClient
}

var (
	_ bridgev2.RemoteMessage                  = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReaction                 = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReactionRemove           = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEdit                     = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageRemove            = (*WAMessageEvent)(nil)
)

func (evt *WAMessageEvent) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	// only change text and captions
	if len(existing) > 1 {
		zerolog.Ctx(ctx).Warn().Msg("Got edit to message with multiple parts")
	}
	editedMsg := evt.Message.Message.GetProtocolMessage().GetEditedMessage()

	cm := evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, editedMsg, nil) // for media messages, it is better not to do this
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
	if reactionMsg := evt.Message.Message.GetReactionMessage(); reactionMsg != nil {
		return reactionMsg.GetText(), ""
	} else {
		return "", ""
	}
}

func (evt *WAMessageEvent) GetRemovedEmojiID() networkid.EmojiID {
	return ""
}

func (evt *WAMessageEvent) ShouldCreatePortal() bool {
	return true
}

func (evt *WAMessageEvent) GetType() bridgev2.RemoteEventType {
	waMsg := evt.Message.Message
	if reactionMsg := waMsg.GetReactionMessage(); reactionMsg != nil {
		if reactionMsg.GetText() == "" {
			return bridgev2.RemoteEventReactionRemove
		}
		return bridgev2.RemoteEventReaction
	} else if protocolMsg := waMsg.GetProtocolMessage(); protocolMsg != nil {
		protocolType := protocolMsg.GetType()
		if protocolType == 0 { // REVOKE (message deletes)
			return bridgev2.RemoteEventMessageRemove
		} else if protocolType == 14 { // Message edits
			return bridgev2.RemoteEventEdit
		}
	}
	return bridgev2.RemoteEventMessage
}

func (evt *WAMessageEvent) GetPortalKey() networkid.PortalKey {
	return evt.portalKey
}

func (evt *WAMessageEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.Str("message_id", evt.Info.ID).Uint64("sender_id", evt.Info.Sender.UserInt())
}

func (evt *WAMessageEvent) GetSender() bridgev2.EventSender {
	return evt.wa.makeEventSender(&evt.Info.Sender)
}

func (evt *WAMessageEvent) GetID() networkid.MessageID {
	return waid.MakeMessageID(evt.Info.Chat, evt.Info.Sender, evt.Info.ID)
}

func (evt *WAMessageEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	return evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, evt.Message.Message, &evt.Message.Info), nil
}
