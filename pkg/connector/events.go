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
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) getPortalKeyByMessageSource(ms types.MessageSource) networkid.PortalKey {
	jid := ms.Chat
	if ms.IsIncomingBroadcast() {
		if ms.IsFromMe {
			jid = ms.BroadcastListOwner.ToNonAD()
		} else {
			jid = ms.Sender.ToNonAD()
		}
	}
	return wa.makeWAPortalKey(jid)
}

type MessageInfoWrapper struct {
	Info types.MessageInfo
	wa   *WhatsAppClient
}

func (evt *MessageInfoWrapper) ShouldCreatePortal() bool {
	return true
}

func (evt *MessageInfoWrapper) GetPortalKey() networkid.PortalKey {
	return evt.wa.getPortalKeyByMessageSource(evt.Info.MessageSource)
}

func (evt *MessageInfoWrapper) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.Str("message_id", evt.Info.ID).Stringer("sender_id", evt.Info.Sender)
}

func (evt *MessageInfoWrapper) GetTimestamp() time.Time {
	return evt.Info.Timestamp
}

func (evt *MessageInfoWrapper) GetSender() bridgev2.EventSender {
	return evt.wa.makeEventSender(evt.Info.Sender)
}

func (evt *MessageInfoWrapper) GetID() networkid.MessageID {
	return waid.MakeMessageID(evt.Info.Chat, evt.Info.Sender, evt.Info.ID)
}

func (evt *MessageInfoWrapper) GetTransactionID() networkid.TransactionID {
	return networkid.TransactionID(evt.GetID())
}

type WAMessageEvent struct {
	*MessageInfoWrapper
	Message  *waE2E.Message
	MsgEvent *events.Message

	parsedMessageType             string
	isUndecryptableUpsertSubEvent bool
}

var (
	_ bridgev2.RemoteMessage                  = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageUpsert            = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageWithTransactionID = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventWithTimestamp       = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReaction                 = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReactionRemove           = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReactionWithMeta         = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEdit                     = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageRemove            = (*WAMessageEvent)(nil)
)

func (evt *WAMessageEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	return evt.MessageInfoWrapper.AddLogContext(c).Str("parsed_message_type", evt.parsedMessageType)
}

func (evt *WAMessageEvent) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	if len(existing) > 1 {
		zerolog.Ctx(ctx).Warn().Msg("Got edit to message with multiple parts")
	}
	var editedMsg *waE2E.Message
	if evt.isUndecryptableUpsertSubEvent {
		// TODO db metadata needs to be updated in this case to remove the error
		editedMsg = evt.Message
	} else {
		editedMsg = evt.Message.GetProtocolMessage().GetEditedMessage()
	}

	// TODO edits to media captions may not contain the media
	cm := evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, editedMsg)
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{cm.Parts[0].ToEditPart(existing[0])},
	}, nil
}

func (evt *WAMessageEvent) GetTargetMessage() networkid.MessageID {
	if reactionMsg := evt.Message.GetReactionMessage(); reactionMsg != nil {
		key := reactionMsg.GetKey()
		senderID := key.GetParticipant()
		if senderID == "" {
			if key.GetFromMe() {
				senderID = evt.Info.Sender.ToNonAD().String()
			} else {
				senderID = evt.wa.Client.Store.ID.ToNonAD().String() // could be false in groups
			}
		}
		senderJID, _ := types.ParseJID(senderID)
		return waid.MakeMessageID(evt.Info.Chat, senderJID, *key.ID)
	} else if protocolMsg := evt.Message.GetProtocolMessage(); protocolMsg != nil {
		key := protocolMsg.GetKey()
		senderID := key.GetParticipant()
		if senderID == "" {
			if key.GetFromMe() {
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
	return evt.Message.GetReactionMessage().GetText(), ""
}

func (evt *WAMessageEvent) GetReactionDBMetadata() any {
	return &ReactionMetadata{
		SenderDeviceID: evt.Info.Sender.Device,
	}
}

func (evt *WAMessageEvent) GetRemovedEmojiID() networkid.EmojiID {
	return ""
}

func (evt *WAMessageEvent) GetType() bridgev2.RemoteEventType {
	switch evt.parsedMessageType {
	case "reaction":
		return bridgev2.RemoteEventReaction
	case "reaction remove":
		return bridgev2.RemoteEventReactionRemove
	case "edit":
		return bridgev2.RemoteEventEdit
	case "revoke":
		return bridgev2.RemoteEventMessageRemove
	case "ignore":
		return bridgev2.RemoteEventUnknown
	default:
		return bridgev2.RemoteEventMessageUpsert
	}
}

func (evt *WAMessageEvent) HandleExisting(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (bridgev2.UpsertResult, error) {
	if existing[0].Metadata.(*MessageMetadata).Error == MsgErrDecryptionFailed {
		zerolog.Ctx(ctx).Debug().Stringer("existing_mxid", existing[0].MXID).Msg("Ignoring duplicate message")
		evt.isUndecryptableUpsertSubEvent = true
		return bridgev2.UpsertResult{SubEvents: []bridgev2.RemoteEvent{
			&WANowDecryptableMessage{WAMessageEvent: evt, editParts: existing}},
		}, nil
	}
	zerolog.Ctx(ctx).Debug().Stringer("existing_mxid", existing[0].MXID).Msg("Ignoring duplicate message")
	return bridgev2.UpsertResult{}, nil
}

func (evt *WAMessageEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	converted := evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, evt.Message)
	for _, part := range converted.Parts {
		part.DBMetadata = &MessageMetadata{
			SenderDeviceID: evt.Info.Sender.Device,
		}
		if evt.Info.IsIncomingBroadcast() {
			part.DBMetadata.(*MessageMetadata).BroadcastListJID = &evt.Info.Chat
			if part.Extra == nil {
				part.Extra = map[string]any{}
			}
			part.Extra["fi.mau.whatsapp.source_broadcast_list"] = evt.Info.Chat.String()
		}
	}
	return converted, nil
}

type WANowDecryptableMessage struct {
	*WAMessageEvent
	editParts []*database.Message
}

var (
	_ bridgev2.RemoteEdit                  = (*WANowDecryptableMessage)(nil)
	_ bridgev2.RemoteEventWithBundledParts = (*WANowDecryptableMessage)(nil)
)

func (evt *WANowDecryptableMessage) GetTargetDBMessage() []*database.Message {
	return evt.editParts
}

func (evt *WANowDecryptableMessage) GetTargetMessage() networkid.MessageID {
	return evt.GetID()
}

func (evt *WANowDecryptableMessage) AddLogContext(c zerolog.Context) zerolog.Context {
	return c
}

func (evt *WANowDecryptableMessage) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventMessage
}

type WAUndecryptableMessage struct {
	*MessageInfoWrapper
}

var (
	_ bridgev2.RemoteMessage                  = (*WAUndecryptableMessage)(nil)
	_ bridgev2.RemoteMessageWithTransactionID = (*WAUndecryptableMessage)(nil)
	_ bridgev2.RemoteEventWithTimestamp       = (*WAUndecryptableMessage)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*WAUndecryptableMessage)(nil)
)

func (evt *WAUndecryptableMessage) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventMessage
}

const UndecryptableMessageNotice = "Decrypting message from WhatsApp failed, waiting for sender to re-send... " +
	"([learn more](https://faq.whatsapp.com/general/security-and-privacy/seeing-waiting-for-this-message-this-may-take-a-while))"

var undecryptableMessageContent event.MessageEventContent

func init() {
	undecryptableMessageContent = format.RenderMarkdown(UndecryptableMessageNotice, true, false)
	undecryptableMessageContent.MsgType = event.MsgNotice
}

func (evt *WAUndecryptableMessage) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	extra := map[string]any{
		"fi.mau.whatsapp.undecryptable": true,
	}
	var broadcastListJID *types.JID
	if evt.Info.IsIncomingBroadcast() {
		broadcastListJID = &evt.Info.Chat
		extra["fi.mau.whatsapp.source_broadcast_list"] = evt.Info.Chat.String()
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: &undecryptableMessageContent,
			Extra:   extra,
			DBMetadata: &MessageMetadata{
				SenderDeviceID:   evt.Info.Sender.Device,
				Error:            MsgErrDecryptionFailed,
				BroadcastListJID: broadcastListJID,
			},
		}},
		Disappear: portal.Disappear,
	}, nil
}
