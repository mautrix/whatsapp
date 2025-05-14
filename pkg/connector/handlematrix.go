package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/util/variationselector"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var (
	_ bridgev2.TypingHandlingNetworkAPI      = (*WhatsAppClient)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*WhatsAppClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*WhatsAppClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*WhatsAppClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*WhatsAppClient)(nil)
	_ bridgev2.PollHandlingNetworkAPI        = (*WhatsAppClient)(nil)
)

func (wa *WhatsAppClient) HandleMatrixPollStart(ctx context.Context, msg *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	waMsg, optionMap, err := wa.Main.MsgConv.PollStartToWhatsApp(ctx, msg.Content, msg.ReplyTo, msg.Portal)
	if err != nil {
		return nil, fmt.Errorf("failed to convert poll vote: %w", err)
	}
	resp, err := wa.handleConvertedMatrixMessage(ctx, &msg.MatrixMessage, waMsg, nil)
	if err != nil {
		return nil, err
	}
	resp.DB.Metadata.(*waid.MessageMetadata).IsMatrixPoll = true
	resp.PostSave = func(ctx context.Context, message *database.Message) {
		err := wa.Main.DB.PollOption.Put(ctx, message.MXID, optionMap)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to save poll options")
		}
	}
	return resp, nil
}

func (wa *WhatsAppClient) HandleMatrixPollVote(ctx context.Context, msg *bridgev2.MatrixPollVote) (*bridgev2.MatrixMessageResponse, error) {
	waMsg, err := wa.Main.MsgConv.PollVoteToWhatsApp(ctx, wa.Client, msg.Content, msg.VoteTo)
	if err != nil {
		return nil, fmt.Errorf("failed to convert poll vote: %w", err)
	}
	return wa.handleConvertedMatrixMessage(ctx, &msg.MatrixMessage, waMsg, nil)
}

func (wa *WhatsAppClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	waMsg, req, err := wa.Main.MsgConv.ToWhatsApp(ctx, wa.Client, msg.Event, msg.Content, msg.ReplyTo, msg.ThreadRoot, msg.Portal)
	if err != nil {
		return nil, fmt.Errorf("failed to convert message: %w", err)
	}
	return wa.handleConvertedMatrixMessage(ctx, msg, waMsg, req)
}

var ErrBroadcastSendDisabled = bridgev2.WrapErrorInStatus(errors.New("sending status messages is disabled")).WithErrorAsMessage().WithIsCertain(true).WithSendNotice(true).WithErrorReason(event.MessageStatusUnsupported)
var ErrBroadcastReactionUnsupported = bridgev2.WrapErrorInStatus(errors.New("reacting to status messages is not currently supported")).WithErrorAsMessage().WithIsCertain(true).WithSendNotice(true).WithErrorReason(event.MessageStatusUnsupported)

func (wa *WhatsAppClient) handleConvertedMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage, waMsg *waE2E.Message, req *whatsmeow.SendRequestExtra) (*bridgev2.MatrixMessageResponse, error) {
	if req == nil {
		req = &whatsmeow.SendRequestExtra{}
	}
	if strings.HasPrefix(string(msg.InputTransactionID), whatsmeow.WebMessageIDPrefix) {
		req.ID = types.MessageID(msg.InputTransactionID)
	} else {
		req.ID = wa.Client.GenerateMessageID()
	}

	chatJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return nil, err
	}
	if chatJID == types.StatusBroadcastJID && wa.Main.Config.DisableStatusBroadcastSend {
		return nil, ErrBroadcastSendDisabled
	}
	wrappedMsgID := waid.MakeMessageID(chatJID, wa.JID, req.ID)
	wrappedMsgID2 := waid.MakeMessageID(chatJID, wa.GetStore().GetLID(), req.ID)
	msg.AddPendingToIgnore(networkid.TransactionID(wrappedMsgID))
	msg.AddPendingToIgnore(networkid.TransactionID(wrappedMsgID2))
	resp, err := wa.Client.SendMessage(ctx, chatJID, waMsg, *req)
	if err != nil {
		return nil, err
	}
	var pickedMessageID networkid.MessageID
	if resp.Sender == wa.GetStore().GetLID() {
		pickedMessageID = wrappedMsgID2
		msg.RemovePending(networkid.TransactionID(wrappedMsgID))
	} else {
		pickedMessageID = wrappedMsgID
		msg.RemovePending(networkid.TransactionID(wrappedMsgID2))
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        pickedMessageID,
			SenderID:  waid.MakeUserID(resp.Sender),
			Timestamp: resp.Timestamp,
			Metadata: &waid.MessageMetadata{
				SenderDeviceID: wa.JID.Device,
			},
		},
		StreamOrder:   resp.Timestamp.Unix(),
		RemovePending: networkid.TransactionID(pickedMessageID),
	}, nil
}

func (wa *WhatsAppClient) PreHandleMatrixReaction(_ context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("failed to parse portal ID: %w", err)
	} else if portalJID == types.StatusBroadcastJID {
		return bridgev2.MatrixReactionPreResponse{}, ErrBroadcastReactionUnsupported
	}
	sender := wa.JID
	if portalJID.Server == types.HiddenUserServer ||
		msg.Portal.Metadata.(*waid.PortalMetadata).CommunityAnnouncementGroup ||
		msg.Portal.Metadata.(*waid.PortalMetadata).AddressingMode == types.AddressingModeLID {
		sender = wa.GetStore().GetLID()
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     waid.MakeUserID(sender),
		Emoji:        variationselector.Remove(msg.Content.RelatesTo.Key),
		MaxReactions: 1,
	}, nil
}

func (wa *WhatsAppClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	messageID, err := waid.ParseMessageID(msg.TargetMessage.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse target message ID: %w", err)
	}

	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse portal ID: %w", err)
	}
	reactionMsg := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:               wa.messageIDToKey(messageID),
			Text:              proto.String(msg.PreHandleResp.Emoji),
			SenderTimestampMS: proto.Int64(msg.Event.Timestamp),
		},
	}
	var req whatsmeow.SendRequestExtra
	if msg.Portal.Metadata.(*waid.PortalMetadata).CommunityAnnouncementGroup {
		reactionMsg.EncReactionMessage, err = wa.Client.EncryptReaction(ctx, msgconv.MessageIDToInfo(wa.Client, messageID), reactionMsg.ReactionMessage)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt reaction: %w", err)
		}
		reactionMsg.ReactionMessage = nil
		req.Meta = &types.MsgMetaInfo{
			DeprecatedLIDSession: ptr.Ptr(false),
		}
	}

	resp, err := wa.Client.SendMessage(ctx, portalJID, reactionMsg, req)
	zerolog.Ctx(ctx).Trace().Any("response", resp).Msg("WhatsApp reaction response")
	return &database.Reaction{
		Metadata: &waid.ReactionMetadata{
			SenderDeviceID: wa.JID.Device,
		},
	}, err
}

func (wa *WhatsAppClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	messageID, err := waid.ParseMessageID(msg.TargetReaction.MessageID)
	if err != nil {
		return fmt.Errorf("failed to parse target message ID: %w", err)
	}

	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return fmt.Errorf("failed to parse portal ID: %w", err)
	}

	reactionMsg := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:               wa.messageIDToKey(messageID),
			Text:              proto.String(""),
			SenderTimestampMS: proto.Int64(msg.Event.Timestamp),
		},
	}

	extra := whatsmeow.SendRequestExtra{}
	if strings.HasPrefix(string(msg.InputTransactionID), whatsmeow.WebMessageIDPrefix) {
		extra.ID = types.MessageID(msg.InputTransactionID)
	}

	resp, err := wa.Client.SendMessage(ctx, portalJID, reactionMsg, extra)
	zerolog.Ctx(ctx).Trace().Any("response", resp).Msg("WhatsApp reaction response")
	return err
}

func (wa *WhatsAppClient) HandleMatrixEdit(ctx context.Context, edit *bridgev2.MatrixEdit) error {
	log := zerolog.Ctx(ctx)

	var editID types.MessageID
	if strings.HasPrefix(string(edit.InputTransactionID), whatsmeow.WebMessageIDPrefix) {
		editID = types.MessageID(edit.InputTransactionID)
	} else {
		editID = wa.Client.GenerateMessageID()
	}

	messageID, err := waid.ParseMessageID(edit.EditTarget.ID)
	if err != nil {
		return fmt.Errorf("failed to parse target message ID: %w", err)
	}

	portalJID, err := waid.ParsePortalID(edit.Portal.ID)
	if err != nil {
		return fmt.Errorf("failed to parse portal ID: %w", err)
	}

	waMsg, _, err := wa.Main.MsgConv.ToWhatsApp(ctx, wa.Client, edit.Event, edit.Content, nil, nil, edit.Portal)
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
		return fmt.Errorf("failed to parse target message ID: %w", err)
	}

	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return fmt.Errorf("failed to parse portal ID: %w", err)
	}

	revokeMessage := wa.Client.BuildRevoke(messageID.Chat, messageID.Sender, messageID.ID)

	extra := whatsmeow.SendRequestExtra{}
	if strings.HasPrefix(string(msg.InputTransactionID), whatsmeow.WebMessageIDPrefix) {
		extra.ID = types.MessageID(msg.InputTransactionID)
	}

	resp, err := wa.Client.SendMessage(ctx, portalJID, revokeMessage, extra)
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
		return fmt.Errorf("failed to parse portal ID: %w", err)
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
		if parsed.Sender.User == wa.GetStore().GetLID().User || parsed.Sender.User == wa.JID.User {
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

func (wa *WhatsAppClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (bool, error) {
	portalJID, err := waid.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return false, err
	}

	if msg.Portal.RoomType == database.RoomTypeDM {
		switch msg.Type {
		case bridgev2.Invite:
			return false, fmt.Errorf("cannot invite additional user to dm")
		default:
			return false, nil
		}
	}

	changes := make([]types.JID, 1)
	var action whatsmeow.ParticipantChange

	switch msg.Type {
	case bridgev2.Invite:
		action = whatsmeow.ParticipantChangeAdd
	case bridgev2.Leave, bridgev2.Kick:
		action = whatsmeow.ParticipantChangeRemove
	default:
		return false, nil
	}

	switch target := msg.Target.(type) {
	case *bridgev2.Ghost:
		changes[0] = waid.ParseUserID(target.ID)
	case *bridgev2.UserLogin:
		ghost, err := target.Bridge.GetGhostByID(ctx, networkid.UserID(target.ID))
		if err != nil {
			return false, fmt.Errorf("failed to get ghost for user: %w", err)
		}
		changes[0] = waid.ParseUserID(ghost.ID)
	default:
		return false, fmt.Errorf("cannot get target intent: unknown type: %T", target)
	}

	_, err = wa.Client.UpdateGroupParticipants(portalJID, changes, action)
	if err != nil {
		return false, err
	}

	return true, nil
}
