package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/variationselector"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

var (
	_ bridgev2.TypingHandlingNetworkAPI      = (*WhatsAppClient)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*WhatsAppClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*WhatsAppClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*WhatsAppClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*WhatsAppClient)(nil)
)

func (wa *WhatsAppClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	waMsg, err := wa.Main.MsgConv.ToWhatsApp(ctx, wa.Client, msg.Event, msg.Content, msg.ReplyTo, msg.Portal)
	if err != nil {
		return nil, fmt.Errorf("failed to convert message: %w", err)
	}
	messageID := wa.Client.GenerateMessageID()
	chatJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return nil, err
	}
	wrappedMsgID := waid.MakeMessageID(chatJID, wa.JID, messageID)
	msg.AddPendingToIgnore(networkid.TransactionID(wrappedMsgID))
	resp, err := wa.Client.SendMessage(ctx, chatJID, waMsg, whatsmeow.SendRequestExtra{
		ID: messageID,
	})
	if err != nil {
		return nil, err
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        wrappedMsgID,
			SenderID:  networkid.UserID(wa.UserLogin.ID),
			Timestamp: resp.Timestamp,
		},
		RemovePending: networkid.TransactionID(wrappedMsgID),
	}, nil
}

func (wa *WhatsAppClient) PreHandleMatrixReaction(_ context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     networkid.UserID(wa.UserLogin.ID),
		Emoji:        variationselector.Remove(msg.Content.RelatesTo.Key),
		MaxReactions: 1,
	}, nil
}

func (wa *WhatsAppClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	messageID, err := waid.ParseMessageID(msg.TargetMessage.ID)
	if err != nil {
		return nil, err
	}

	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return nil, err
	}
	reactionMsg := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:               wa.messageIDToKey(messageID),
			Text:              proto.String(msg.PreHandleResp.Emoji),
			SenderTimestampMS: proto.Int64(msg.Event.Timestamp),
		},
	}

	resp, err := wa.Client.SendMessage(ctx, portalJID, reactionMsg)
	zerolog.Ctx(ctx).Trace().Any("response", resp).Msg("WhatsApp reaction response")
	return nil, err
}

func (wa *WhatsAppClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	messageID, err := waid.ParseMessageID(msg.TargetReaction.MessageID)
	if err != nil {
		return err
	}

	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return err
	}

	reactionMsg := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:               wa.messageIDToKey(messageID),
			Text:              proto.String(""),
			SenderTimestampMS: proto.Int64(msg.Event.Timestamp),
		},
	}

	resp, err := wa.Client.SendMessage(ctx, portalJID, reactionMsg)
	zerolog.Ctx(ctx).Trace().Any("response", resp).Msg("WhatsApp reaction response")
	return err
}

func (wa *WhatsAppClient) HandleMatrixEdit(ctx context.Context, edit *bridgev2.MatrixEdit) error {
	log := zerolog.Ctx(ctx)
	editID := wa.Client.GenerateMessageID()

	messageID, err := waid.ParseMessageID(edit.EditTarget.ID)
	if err != nil {
		return err
	}

	portalJID, err := waid.ParsePortalID(edit.Portal.ID)
	if err != nil {
		return err
	}

	waMsg, err := wa.Main.MsgConv.ToWhatsApp(ctx, wa.Client, edit.Event, edit.Content, nil, edit.Portal)
	if err != nil {
		return fmt.Errorf("failed to convert message: %w", err)
	}
	convertedEdit := wa.Client.BuildEdit(messageID.Chat, messageID.ID, waMsg)
	if edit.OrigSender == nil {
		convertedEdit.EditedMessage.Message.ProtocolMessage.TimestampMS = proto.Int64(edit.Event.Timestamp)
	}

	//wrappedMsgID := waid.MakeMessageID(portalJID, wa.JID, messageID)
	//edit.AddPendingToIgnore(networkid.TransactionID(wrappedMsgID))
	resp, err := wa.Client.SendMessage(ctx, portalJID, convertedEdit, whatsmeow.SendRequestExtra{
		ID: editID,
	})
	log.Trace().Any("response", resp).Msg("WhatsApp edit response")
	return err
}

func (wa *WhatsAppClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	log := zerolog.Ctx(ctx)
	messageID, err := waid.ParseMessageID(msg.TargetMessage.ID)
	if err != nil {
		return err
	}

	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return err
	}

	revokeMessage := wa.Client.BuildRevoke(messageID.Chat, messageID.Sender, messageID.ID)

	resp, err := wa.Client.SendMessage(ctx, portalJID, revokeMessage)
	log.Trace().Any("response", resp).Msg("WhatsApp delete response")
	return err
}

func (wa *WhatsAppClient) HandleMatrixReadReceipt(ctx context.Context, receipt *bridgev2.MatrixReadReceipt) error {
	if !receipt.ReadUpTo.After(receipt.LastRead) {
		return nil
	}
	if receipt.LastRead.IsZero() {
		receipt.LastRead = receipt.ReadUpTo.Add(-5 * time.Second)
	}
	portalJID, err := waid.ParsePortalID(receipt.Portal.ID)
	if err != nil {
		return err
	}
	messages, err := receipt.Portal.Bridge.DB.Message.GetMessagesBetweenTimeQuery(ctx, receipt.Portal.PortalKey, receipt.LastRead, receipt.ReadUpTo)
	if err != nil {
		return fmt.Errorf("failed to get messages to mark as read: %w", err)
	} else if len(messages) == 0 {
		return nil
	}
	log := zerolog.Ctx(ctx)
	log.Trace().
		Time("last_read", receipt.LastRead).
		Time("read_up_to", receipt.ReadUpTo).
		Int("message_count", len(messages)).
		Msg("Handling read receipt")
	messagesToRead := make(map[types.JID][]string)
	for _, msg := range messages {
		parsed, err := waid.ParseMessageID(msg.ID)
		if err != nil {
			continue
		}
		if msg.SenderID == networkid.UserID(wa.UserLogin.ID) {
			continue
		}
		var key types.JID
		// In group chats, group receipts by sender. In DMs, just use blank key (no participant field).
		if parsed.Sender != parsed.Chat {
			key = parsed.Sender
		}
		messagesToRead[key] = append(messagesToRead[key], parsed.ID)
	}
	for messageSender, ids := range messagesToRead {
		err = wa.Client.MarkRead(ids, receipt.Receipt.Timestamp, portalJID, messageSender)
		if err != nil {
			log.Err(err).Strs("ids", ids).Msg("Failed to mark messages as read")
		}
	}
	return err
}

func (wa *WhatsAppClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return err
	}
	var chatPresence types.ChatPresence
	var mediaPresence types.ChatPresenceMedia
	if msg.IsTyping {
		chatPresence = types.ChatPresenceComposing
	} else {
		chatPresence = types.ChatPresencePaused
	}
	switch msg.Type {
	case bridgev2.TypingTypeText:
		mediaPresence = types.ChatPresenceMediaText
	case bridgev2.TypingTypeRecordingMedia:
		mediaPresence = types.ChatPresenceMediaAudio
	case bridgev2.TypingTypeUploadingMedia:
		return nil
	}

	if wa.Main.Config.SendPresenceOnTyping {
		err = wa.Client.SendPresence(types.PresenceAvailable)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to set presence on typing")
		}
	}
	return wa.Client.SendChatPresence(portalJID, chatPresence, mediaPresence)
}
