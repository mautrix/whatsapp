package connector

import (
	"context"
	"fmt"

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
	chatJID, err := types.ParseJID(string(msg.Portal.ID))
	if err != nil {
		return nil, err
	}
	senderJID := wa.Device.ID
	resp, err := wa.Client.SendMessage(ctx, chatJID, waMsg, whatsmeow.SendRequestExtra{
		ID: messageID,
	})
	if err != nil {
		return nil, err
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        waid.MakeMessageID(chatJID, senderJID.ToNonAD(), messageID),
			SenderID:  networkid.UserID(wa.UserLogin.ID),
			Timestamp: resp.Timestamp,
		},
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

	portalJID, err := types.ParseJID(string(msg.Portal.ID))
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

	portalJID, err := types.ParseJID(string(msg.Portal.ID))
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
	messageID, err := waid.ParseMessageID(edit.EditTarget.ID)
	if err != nil {
		return err
	}

	portalJID, err := types.ParseJID(string(edit.Portal.ID))
	if err != nil {
		return err
	}

	//TODO: DO CONVERSION VIA msgconv FUNC
	//TODO: IMPLEMENT MEDIA CAPTION EDITS
	editMessage := wa.Client.BuildEdit(portalJID, messageID.ID, &waE2E.Message{
		Conversation: proto.String(edit.Content.Body),
	})

	resp, err := wa.Client.SendMessage(ctx, portalJID, editMessage)
	log.Trace().Any("response", resp).Msg("WhatsApp edit response")
	return err
}

func (wa *WhatsAppClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	log := zerolog.Ctx(ctx)
	messageID, err := waid.ParseMessageID(msg.TargetMessage.ID)
	if err != nil {
		return err
	}

	portalJID, err := types.ParseJID(string(msg.Portal.ID))
	if err != nil {
		return err
	}

	revokeMessage := wa.Client.BuildRevoke(messageID.Chat, messageID.Sender, messageID.ID)

	resp, err := wa.Client.SendMessage(ctx, portalJID, revokeMessage)
	log.Trace().Any("response", resp).Msg("WhatsApp delete response")
	return err
}

func (wa *WhatsAppClient) HandleMatrixReadReceipt(_ context.Context, _ *bridgev2.MatrixReadReceipt) error {
	return nil
}
