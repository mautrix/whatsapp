// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
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
	return evt.wa.makeEventSender(evt.wa.Main.Bridge.BackgroundCtx, evt.Info.Sender)
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
	postHandle                    func()
}

var (
	_ bridgev2.RemoteMessage                  = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageUpsert            = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageWithTransactionID = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventWithTimestamp       = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventThatMayCreatePortal = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEventWithStreamOrder     = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReaction                 = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReactionRemove           = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteReactionWithMeta         = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteEdit                     = (*WAMessageEvent)(nil)
	_ bridgev2.RemoteMessageRemove            = (*WAMessageEvent)(nil)
	_ bridgev2.RemotePostHandler              = (*WAMessageEvent)(nil)
)

func (evt *WAMessageEvent) GetStreamOrder() int64 {
	return evt.Info.Timestamp.Unix()
}

func (evt *WAMessageEvent) isViewOnce() bool {
	return evt.MsgEvent.IsViewOnce || evt.MsgEvent.IsViewOnceV2 || evt.MsgEvent.IsViewOnceV2Extension
}

func (evt *WAMessageEvent) AddLogContext(c zerolog.Context) zerolog.Context {
	if targetMsg := evt.GetTargetMessage(); targetMsg != "" {
		c = c.Str("target_message_id", string(targetMsg))
	}
	return evt.MessageInfoWrapper.AddLogContext(c).Str("parsed_message_type", evt.parsedMessageType)
}

func (evt *WAMessageEvent) PreHandle(ctx context.Context, portal *bridgev2.Portal) {
	if evt.Info.AddressingMode != types.AddressingModeLID || evt.Info.Chat.Server != types.GroupServer {
		return
	}
	portalJID, err := waid.ParsePortalID(portal.ID)
	if err != nil {
		return
	}
	meta := portal.Metadata.(*waid.PortalMetadata)
	if meta.AddressingMode == types.AddressingModeLID || meta.LIDMigrationAttempted {
		return
	}
	log := zerolog.Ctx(ctx).With().Str("action", "group lid migration").Logger()
	ctx = log.WithContext(ctx)
	meta.LIDMigrationAttempted = true
	info, err := evt.wa.Client.GetGroupInfo(portalJID)
	if err != nil {
		log.Err(err).Msg("Failed to get group info for lid migration")
		return
	}
	if info.AddressingMode != types.AddressingModeLID {
		log.Warn().Msg("Received LID message, but group addressing mode isn't set to LID? Not migrating")
		return
	}
	log.Info().Msg("Resyncing group members as it appears to have switched to LID addressing mode")
	portal.UpdateInfo(ctx, evt.wa.wrapGroupInfo(ctx, info), evt.wa.UserLogin, nil, time.Time{})
	log.Debug().Msg("Finished resyncing after LID change")
	if evt.Info.Sender.Server == types.DefaultUserServer && evt.Info.SenderAlt.Server == types.HiddenUserServer {
		evt.Info.Sender, evt.Info.SenderAlt = evt.Info.SenderAlt, evt.Info.Sender
		log.Debug().
			Stringer("new_sender", evt.Info.Sender).
			Stringer("new_sender_alt", evt.Info.SenderAlt).
			Msg("Overriding sender to LID after resyncing group members")
	}
}

func (evt *WAMessageEvent) PostHandle(ctx context.Context, portal *bridgev2.Portal) {
	if ph := evt.postHandle; ph != nil {
		evt.postHandle = nil
		ph()
	}
}

func (evt *WAMessageEvent) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	if len(existing) > 1 {
		zerolog.Ctx(ctx).Warn().Msg("Got edit to message with multiple parts")
	}
	var editedMsg *waE2E.Message
	var previouslyConvertedPart *bridgev2.ConvertedMessagePart
	if evt.isUndecryptableUpsertSubEvent {
		// TODO db metadata needs to be updated in this case to remove the error
		editedMsg = evt.Message
	} else {
		editedMsg = evt.Message.GetProtocolMessage().GetEditedMessage()
		previouslyConvertedPart = evt.wa.Main.GetMediaEditCache(portal, evt.GetTargetMessage())
		meta := existing[0].Metadata.(*waid.MessageMetadata)
		if slices.Contains(meta.Edits, evt.Info.ID) {
			return nil, fmt.Errorf("%w: edit already handled", bridgev2.ErrIgnoringRemoteEvent)
		}
		meta.Edits = append(meta.Edits, evt.Info.ID)
	}

	ctx = context.WithValue(ctx, msgconv.ContextKeyEditTargetID, evt.Message.GetProtocolMessage().GetKey().GetID())
	cm := evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, editedMsg, &evt.Info, evt.isViewOnce(), previouslyConvertedPart)
	if evt.isUndecryptableUpsertSubEvent && isFailedMedia(cm) {
		evt.postHandle = func() {
			evt.wa.processFailedMedia(ctx, portal.PortalKey, evt.GetID(), cm, false)
		}
	}
	editPart := cm.Parts[0].ToEditPart(existing[0])
	if evt.isUndecryptableUpsertSubEvent {
		if editPart.TopLevelExtra == nil {
			editPart.TopLevelExtra = make(map[string]any)
		}
		editPart.TopLevelExtra["com.beeper.dont_render_edited"] = true
	}
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{editPart},
	}, nil
}

func (evt *WAMessageEvent) GetTargetMessage() networkid.MessageID {
	if reactionMsg := evt.Message.GetReactionMessage(); reactionMsg != nil {
		return msgconv.KeyToMessageID(evt.wa.Client, evt.Info.Chat, evt.Info.Sender, reactionMsg.GetKey())
	} else if protocolMsg := evt.Message.GetProtocolMessage(); protocolMsg != nil {
		return msgconv.KeyToMessageID(evt.wa.Client, evt.Info.Chat, evt.Info.Sender, protocolMsg.GetKey())
	}
	return ""
}

func (evt *WAMessageEvent) GetReactionEmoji() (string, networkid.EmojiID) {
	return evt.Message.GetReactionMessage().GetText(), ""
}

func (evt *WAMessageEvent) GetReactionDBMetadata() any {
	return &waid.ReactionMetadata{
		SenderDeviceID: evt.Info.Sender.Device,
	}
}

func (evt *WAMessageEvent) GetRemovedEmojiID() networkid.EmojiID {
	return ""
}

func (evt *WAMessageEvent) GetType() bridgev2.RemoteEventType {
	switch evt.parsedMessageType {
	case "reaction", "encrypted reaction":
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
	if existing[0].Metadata.(*waid.MessageMetadata).Error == waid.MsgErrDecryptionFailed {
		evt.wa.trackUndecryptableResolved(evt.MsgEvent)
		zerolog.Ctx(ctx).Debug().
			Stringer("existing_mxid", existing[0].MXID).
			Msg("Received decryptable version of previously undecryptable message")
		evt.isUndecryptableUpsertSubEvent = true
		return bridgev2.UpsertResult{SubEvents: []bridgev2.RemoteEvent{
			&WANowDecryptableMessage{WAMessageEvent: evt, editParts: existing}},
		}, nil
	}
	zerolog.Ctx(ctx).Debug().Stringer("existing_mxid", existing[0].MXID).Msg("Ignoring duplicate message")
	return bridgev2.UpsertResult{}, nil
}

func (evt *WAMessageEvent) ConvertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI) (*bridgev2.ConvertedMessage, error) {
	evt.wa.EnqueuePortalResync(portal)
	converted := evt.wa.Main.MsgConv.ToMatrix(ctx, portal, evt.wa.Client, intent, evt.Message, &evt.Info, evt.isViewOnce(), nil)
	if isFailedMedia(converted) {
		evt.postHandle = func() {
			evt.wa.processFailedMedia(ctx, portal.PortalKey, evt.GetID(), converted, false)
		}
	} else if len(converted.Parts) > 0 {
		evt.wa.Main.AddMediaEditCache(portal, evt.GetID(), converted.Parts[0])
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
	return bridgev2.RemoteEventEdit
}

type WAUndecryptableMessage struct {
	*MessageInfoWrapper
	Type events.UnavailableType
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
	content := &undecryptableMessageContent
	if evt.Type == events.UnavailableTypeViewOnce {
		content = &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    "You received a view once message. For added privacy, you can only open it on the WhatsApp app.",
		}
	}
	// TODO thread root for comments
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:    event.EventMessage,
			Content: content,
			Extra:   extra,
			DBMetadata: &waid.MessageMetadata{
				SenderDeviceID:   evt.Info.Sender.Device,
				Error:            waid.MsgErrDecryptionFailed,
				BroadcastListJID: broadcastListJID,
			},
		}},
		Disappear: portal.Disappear,
	}, nil
}

func (evt *WAUndecryptableMessage) GetStreamOrder() int64 {
	return evt.Info.Timestamp.Unix()
}

type WAMediaRetry struct {
	*events.MediaRetry
	wa *WhatsAppClient
}

func (evt *WAMediaRetry) GetType() bridgev2.RemoteEventType {
	return bridgev2.RemoteEventEdit
}

func (evt *WAMediaRetry) GetPortalKey() networkid.PortalKey {
	return evt.wa.makeWAPortalKey(evt.ChatID)
}

func (evt *WAMediaRetry) AddLogContext(c zerolog.Context) zerolog.Context {
	return c.
		Str("message_id", evt.MessageID).
		Stringer("sender_id", evt.SenderID).
		Stringer("chat_id", evt.ChatID).
		Bool("from_me", evt.FromMe).
		Str("wa_event_type", "media retry")
}

func (evt *WAMediaRetry) getRealSender() types.JID {
	sender := evt.SenderID
	if evt.FromMe {
		sender = evt.wa.JID.ToNonAD()
	} else if sender.IsEmpty() && (evt.ChatID.Server == types.DefaultUserServer || evt.ChatID.Server == types.BotServer) {
		sender = evt.ChatID.ToNonAD()
	}
	return sender
}

func (evt *WAMediaRetry) GetSender() bridgev2.EventSender {
	return evt.wa.makeEventSender(evt.wa.Main.Bridge.BackgroundCtx, evt.getRealSender())
}

func (evt *WAMediaRetry) GetTargetMessage() networkid.MessageID {
	return waid.MakeMessageID(evt.ChatID, evt.getRealSender(), evt.MessageID)
}

func (evt *WAMediaRetry) GetTimestamp() time.Time {
	return evt.Timestamp
}

func (evt *WAMediaRetry) makeErrorEdit(part *database.Message, meta *msgconv.PreparedMedia, err error) *bridgev2.ConvertedEdit {
	content := &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("Failed to bridge media after re-requesting it from your phone: %v", err),
	}
	if meta.FormattedBody != "" {
		content.EnsureHasHTML()
		content.Body += "\n\n" + meta.Body
		content.FormattedBody += "<br><br>" + meta.FormattedBody
	} else if meta.Body != meta.FileName && meta.FileName != "" {
		content.Body += "\n\n" + meta.Body
	}
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{{
			Part:    part,
			Type:    event.EventMessage,
			Content: content,
		}},
	}
}

func (evt *WAMediaRetry) ConvertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message) (*bridgev2.ConvertedEdit, error) {
	meta := existing[0].Metadata.(*waid.MessageMetadata)
	if meta.DirectMediaMeta != nil {
		evt.wa.receiveDirectMediaRetry(ctx, existing[0], evt.MediaRetry)
		return nil, fmt.Errorf("%w: direct media retry", bridgev2.ErrIgnoringRemoteEvent)
	} else if meta.Error != waid.MsgErrMediaNotFound {
		return nil, fmt.Errorf("%w: message doesn't have media error", bridgev2.ErrIgnoringRemoteEvent)
	} else if meta.FailedMediaMeta == nil {
		return nil, fmt.Errorf("%w: message doesn't have media metadata", bridgev2.ErrIgnoringRemoteEvent)
	}
	var mediaMeta msgconv.PreparedMedia
	err := json.Unmarshal(meta.FailedMediaMeta, &mediaMeta)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal media metadata: %w", err)
	}
	log := zerolog.Ctx(ctx)
	retryData, err := whatsmeow.DecryptMediaRetryNotification(evt.MediaRetry, mediaMeta.FailedKeys.Key)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to decrypt media retry notification")
		return evt.makeErrorEdit(existing[0], &mediaMeta, err), nil
	} else if retryData.GetResult() != waMmsRetry.MediaRetryNotification_SUCCESS {
		errorName := waMmsRetry.MediaRetryNotification_ResultType_name[int32(retryData.GetResult())]
		if retryData.GetDirectPath() == "" {
			log.Warn().Str("error_name", errorName).Msg("Got error response in media retry notification")
			log.Debug().Any("error_content", retryData).Msg("Full error response content")
			if retryData.GetResult() == waMmsRetry.MediaRetryNotification_NOT_FOUND {
				return evt.makeErrorEdit(existing[0], &mediaMeta, whatsmeow.ErrMediaNotAvailableOnPhone), nil
			}
			return evt.makeErrorEdit(existing[0], &mediaMeta, fmt.Errorf("phone sent error response: %s", errorName)), nil
		} else {
			log.Debug().Msg("Got error response in media retry notification, but response also contains a new download URL - trying to download")
		}
	}
	err = evt.wa.mediaRetryLock.Acquire(ctx, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire media retry lock: %w", err)
	}
	defer evt.wa.mediaRetryLock.Release(1)

	mediaMeta.FailedKeys.DirectPath = retryData.GetDirectPath()
	return evt.wa.Main.MsgConv.MediaRetryToMatrix(ctx, &mediaMeta, evt.wa.Client, intent, portal, existing[0]), nil
}

var (
	_ bridgev2.RemoteEdit               = (*WAMediaRetry)(nil)
	_ bridgev2.RemoteEventWithTimestamp = (*WAMediaRetry)(nil)
)
