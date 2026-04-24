package connector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var _ bridgev2.BackfillingNetworkAPI = (*WhatsAppClient)(nil)

func (wa *WhatsAppClient) historySyncLoop(ctx context.Context) {
	dispatchTimer := time.NewTimer(wa.Main.Config.HistorySync.DispatchWait)

	var timerPending atomic.Bool
	if !wa.isNewLogin && wa.UserLogin.Metadata.(*waid.UserLoginMetadata).HistorySyncPortalsNeedCreating {
		dispatchTimer.Reset(5 * time.Second)
		timerPending.Store(true)
	} else {
		dispatchTimer.Stop()
	}
	if wa.Client.ManualHistorySyncDownload {
		// Wake up the queue once to check if there are pending notifications
		select {
		case wa.historySyncWakeup <- struct{}{}:
		default:
		}
	}
	wa.UserLogin.Log.Debug().Msg("Starting history sync loops")
	// Separate loop for creating portals to ensure it doesn't block processing new history sync payloads.
	go func() {
		for {
			select {
			case <-dispatchTimer.C:
				timerPending.Store(false)
				wa.createPortalsFromHistorySync(ctx)
			case <-ctx.Done():
				wa.UserLogin.Log.Debug().Msg("Stopping portal creation history sync loop")
				return
			}
		}
	}()
	for {
		var resetTimer bool
		select {
		case <-wa.historySyncWakeup:
			dispatchTimer.Stop()
			notif, rowid, err := wa.Main.DB.HSNotif.GetNext(ctx, wa.UserLogin.ID)
			if err != nil {
				wa.UserLogin.Log.Err(err).Msg("Failed to get next history sync notification")
			} else if notif == nil {
				wa.UserLogin.Log.Debug().Msg("No more queued history sync notifications")
			} else {
				resetTimer = wa.downloadAndSaveWAHistorySyncData(ctx, notif, rowid)
				// Continue waking up the loop until all queued notifications are processed
				select {
				case wa.historySyncWakeup <- struct{}{}:
				default:
				}
			}
		case <-ctx.Done():
			wa.UserLogin.Log.Debug().Msg("Stopping main history sync loop")
			return
		}
		if resetTimer {
			timerPending.Store(true)
		}
		if timerPending.Load() {
			dispatchTimer.Reset(wa.Main.Config.HistorySync.DispatchWait)
		}
	}
}

func (wa *WhatsAppClient) saveWAHistorySyncNotification(ctx context.Context, evt *waE2E.HistorySyncNotification) {
	err := wa.Main.DB.HSNotif.Put(ctx, wa.UserLogin.ID, evt)
	if err != nil {
		wa.UserLogin.Log.Err(err).Msg("Failed to store history sync notification in queue")
		return
	}
	wa.UserLogin.Log.Debug().
		Stringer("sync_type", evt.GetSyncType()).
		Uint32("chunk_order", evt.GetChunkOrder()).
		Uint32("progress", evt.GetProgress()).
		Msg("Stored history sync notification in queue")
	select {
	case wa.historySyncWakeup <- struct{}{}:
	default:
	}
}

func (wa *WhatsAppClient) downloadAndSaveWAHistorySyncData(ctx context.Context, evt *waE2E.HistorySyncNotification, rowid int) (resetTimer bool) {
	log := wa.UserLogin.Log.With().
		Str("action", "download history sync").
		Stringer("sync_type", evt.GetSyncType()).
		Uint32("chunk_order", evt.GetChunkOrder()).
		Uint32("progress", evt.GetProgress()).
		Logger()
	log.Debug().
		Int64("oldest_msg_in_chunk_ts", evt.GetOldestMsgInChunkTimestampSec()).
		Any("full_request_meta", evt.GetFullHistorySyncOnDemandRequestMetadata()).
		Any("access_status", evt.GetMessageAccessStatus()).
		Str("peer_data_request_session_id", evt.GetPeerDataRequestSessionID()).
		Msg("Downloading history sync")
	blob, err := wa.Client.DownloadHistorySync(log.WithContext(ctx), evt, true)
	if err != nil {
		log.Err(err).Msg("Failed to download history sync")
		return
	}
	if blob.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND {
		wa.handleOnDemandHistorySync(ctx, blob)
		if err = wa.Main.DB.HSNotif.Delete(ctx, rowid); err != nil {
			log.Err(err).Msg("Failed to delete queued on-demand history sync notification")
		} else if err = wa.Client.DeleteMedia(ctx, whatsmeow.MediaHistory, evt.GetDirectPath(), evt.GetFileEncSHA256(), evt.GetEncHandle()); err != nil {
			log.Err(err).Msg("Failed to delete history sync blob from server")
		} else {
			log.Debug().Msg("Finished handling on-demand history sync and deleted history sync blob from server")
		}
		return
	}
	err = wa.Main.DB.DoTxn(ctx, nil, func(ctx context.Context) (innerErr error) {
		innerErr = wa.handleWAHistorySync(ctx, evt, blob, true)
		if innerErr != nil {
			return
		}
		innerErr = wa.Main.DB.HSNotif.Delete(ctx, rowid)
		if innerErr != nil {
			innerErr = fmt.Errorf("failed to delete queued history sync notification: %w", innerErr)
		}
		return
	})
	if err != nil {
		log.Err(err).Msg("Failed to store history sync notification data")
	} else {
		resetTimer = blob.GetSyncType() == waHistorySync.HistorySync_INITIAL_BOOTSTRAP ||
			blob.GetSyncType() == waHistorySync.HistorySync_RECENT ||
			blob.GetSyncType() == waHistorySync.HistorySync_FULL
		err = wa.Client.DeleteMedia(ctx, whatsmeow.MediaHistory, evt.GetDirectPath(), evt.GetFileEncSHA256(), evt.GetEncHandle())
		if err != nil {
			log.Err(err).Msg("Failed to delete history sync blob from server")
		} else {
			log.Debug().Msg("Deleted history sync blob from server")
		}
	}
	return
}

func (wa *WhatsAppClient) handleWAHistorySync(
	ctx context.Context,
	notif *waE2E.HistorySyncNotification,
	evt *waHistorySync.HistorySync,
	stopOnError bool,
) error {
	if evt == nil || evt.SyncType == nil {
		return nil
	}
	log := wa.UserLogin.Log.With().
		Str("action", "store history sync").
		Stringer("sync_type", evt.GetSyncType()).
		Uint32("chunk_order", evt.GetChunkOrder()).
		Uint32("progress", evt.GetProgress()).
		Logger()
	ctx = log.WithContext(ctx)
	if evt.GetGlobalSettings() != nil {
		log.Debug().Interface("global_settings", evt.GetGlobalSettings()).Msg("Got global settings in history sync")
	}
	if evt.GetSyncType() == waHistorySync.HistorySync_INITIAL_STATUS_V3 ||
		evt.GetSyncType() == waHistorySync.HistorySync_PUSH_NAME ||
		evt.GetSyncType() == waHistorySync.HistorySync_NON_BLOCKING_DATA {
		if evt.GetSyncType() == waHistorySync.HistorySync_PUSH_NAME {
			wa.pushNamesSynced.Set()
		}
		log.Debug().
			Int("conversation_count", len(evt.GetConversations())).
			Int("pushname_count", len(evt.GetPushnames())).
			Int("status_count", len(evt.GetStatusV3Messages())).
			Int("recent_sticker_count", len(evt.GetRecentStickers())).
			Int("past_participant_count", len(evt.GetPastParticipants())).
			Msg("Ignoring history sync")
		return nil
	}
	log.Info().
		Int("conversation_count", len(evt.GetConversations())).
		Int("past_participant_count", len(evt.GetPastParticipants())).
		Dict("notification_metadata", zerolog.Dict().
			Int64("oldest_msg_in_chunk_ts", notif.GetOldestMsgInChunkTimestampSec()).
			Any("full_request_meta", notif.GetFullHistorySyncOnDemandRequestMetadata()).
			Any("access_status", notif.GetMessageAccessStatus()).
			Str("peer_data_request_session_id", notif.GetPeerDataRequestSessionID())).
		Msg("Storing history sync")
	start := time.Now()
	successfullySavedTotal := 0
	failedToSaveTotal := 0
	totalMessageCount := 0
	for _, conv := range evt.GetConversations() {
		log := log.With().
			Int("msg_count", len(conv.GetMessages())).
			Logger()
		jid, err := types.ParseJID(conv.GetID())
		if err != nil {
			totalMessageCount += len(conv.GetMessages())
			log.Warn().Err(err).
				Str("chat_jid", conv.GetID()).
				Msg("Failed to parse chat JID in history sync")
			continue
		} else if jid.Server == types.BroadcastServer {
			log.Debug().Stringer("chat_jid", jid).Msg("Skipping broadcast list in history sync")
			continue
		} else {
			totalMessageCount += len(conv.GetMessages())
		}
		if jid.Server == types.HiddenUserServer {
			pn, err := wa.GetStore().LIDs.GetPNForLID(ctx, jid)
			if err != nil {
				log.Err(err).Stringer("lid", jid).Msg("Failed to get PN for LID in history sync")
			} else if pn.IsEmpty() {
				log.Warn().Stringer("lid", jid).Msg("No PN found for LID in history sync")
			} else {
				log.Debug().
					Stringer("lid", jid).
					Stringer("pn", pn).
					Msg("Rerouting LID DM to phone number in history sync")
				jid = pn
			}
		}
		log.UpdateContext(func(c zerolog.Context) zerolog.Context {
			return c.Stringer("chat_jid", jid)
		})

		var minTime, maxTime, firstItemTime, lastItemTime time.Time
		var minTimeIndex, maxTimeIndex int

		ignoredTypes := 0
		messages := make([]*wadb.HistorySyncMessageTuple, 0, len(conv.GetMessages()))
		for i, rawMsg := range conv.GetMessages() {
			// Don't store messages that will just be skipped.
			msgEvt, err := wa.Client.ParseWebMessage(jid, rawMsg.GetMessage())
			if err != nil {
				log.Warn().Err(err).
					Int("msg_index", i).
					Str("msg_id", rawMsg.GetMessage().GetKey().GetID()).
					Uint64("msg_time_seconds", rawMsg.GetMessage().GetMessageTimestamp()).
					Msg("Dropping historical message due to parse error")
				continue
			}
			if firstItemTime.IsZero() {
				firstItemTime = msgEvt.Info.Timestamp
			}
			lastItemTime = msgEvt.Info.Timestamp
			if minTime.IsZero() || msgEvt.Info.Timestamp.Before(minTime) {
				minTime = msgEvt.Info.Timestamp
				minTimeIndex = i
			}
			if maxTime.IsZero() || msgEvt.Info.Timestamp.After(maxTime) {
				maxTime = msgEvt.Info.Timestamp
				maxTimeIndex = i
			}

			msgType := getMessageType(msgEvt.Message)
			if msgType == "ignore" || strings.HasPrefix(msgType, "unknown_protocol_") {
				ignoredTypes++
				continue
			}
			marshaled, err := proto.Marshal(rawMsg)
			if err != nil {
				log.Warn().Err(err).
					Int("msg_index", i).
					Str("msg_id", msgEvt.Info.ID).
					Msg("Failed to marshal message")
				continue
			}

			messages = append(messages, &wadb.HistorySyncMessageTuple{Info: &msgEvt.Info, Message: marshaled})
		}
		log.Debug().
			Int("wrapped_count", len(messages)).
			Int("ignored_msg_type_count", ignoredTypes).
			Time("lowest_time", minTime).
			Int("lowest_time_index", minTimeIndex).
			Time("highest_time", maxTime).
			Int("highest_time_index", maxTimeIndex).
			Time("first_item_time", firstItemTime).
			Time("last_item_time", lastItemTime).
			Bool("highest_time_mismatch", firstItemTime != maxTime).
			Dict("metadata", zerolog.Dict().
				Uint32("ephemeral_expiration", conv.GetEphemeralExpiration()).
				Int64("ephemeral_setting_timestamp", conv.GetEphemeralSettingTimestamp()).
				Uint64("last_message_ts", conv.GetLastMsgTimestamp()).
				Bool("marked_unread", conv.GetMarkedAsUnread()).
				Bool("archived", conv.GetArchived()).
				Uint32("pinned", conv.GetPinned()).
				Uint64("mute_end", conv.GetMuteEndTime()).
				Uint32("unread_count", conv.GetUnreadCount()).
				Bool("end_of_history", conv.GetEndOfHistoryTransfer()).
				Stringer("end_of_history_type", conv.GetEndOfHistoryTransferType()),
			).
			Msg("Collected messages to save from history sync conversation")

		if len(messages) > 0 {
			err = wa.Main.DB.Conversation.Put(ctx, wadb.NewConversation(wa.UserLogin.ID, jid, conv, maxTime))
			if err != nil {
				if stopOnError {
					return fmt.Errorf("failed to save conversation metadata for %s: %w", jid, err)
				}
				log.Err(err).Msg("Failed to save conversation metadata")
				continue
			}
			err = wa.Main.DB.Message.Put(ctx, wa.UserLogin.ID, jid, messages)
			if err != nil {
				if stopOnError {
					return fmt.Errorf("failed to save messages in %s: %w", jid, err)
				}
				log.Err(err).Msg("Failed to save messages")
				failedToSaveTotal += len(messages)
			} else {
				successfullySavedTotal += len(messages)
			}
			err = wa.Main.Bridge.DB.BackfillTask.MarkNotDone(ctx, wa.makeWAPortalKey(jid), wa.UserLogin.ID)
			if err != nil {
				if stopOnError {
					return fmt.Errorf("failed to mark backfill task as not done for %s: %w", jid, err)
				}
				log.Err(err).Msg("Failed to mark backfill task as not done")
			}
		}
	}
	log.Info().
		Int("total_saved_count", successfullySavedTotal).
		Int("total_failed_count", failedToSaveTotal).
		Int("total_message_count", totalMessageCount).
		Dur("duration", time.Since(start)).
		Msg("Finished storing history sync")
	return nil
}

func (wa *WhatsAppClient) createPortalsFromHistorySync(ctx context.Context) {
	log := wa.UserLogin.Log.With().
		Str("action", "create portals from history sync").
		Logger()
	ctx = log.WithContext(ctx)
	limit := wa.Main.Config.HistorySync.MaxInitialConversations
	loginTS := wa.UserLogin.Metadata.(*waid.UserLoginMetadata).LoggedInAt
	conversations, err := wa.Main.DB.Conversation.GetRecent(ctx, wa.UserLogin.ID, limit, loginTS)
	if err != nil {
		log.Err(err).Msg("Failed to get recent conversations from database")
		return
	}
	log.Info().
		Int("limit", limit).
		Int("conversation_count", len(conversations)).
		Int64("login_timestamp", loginTS.Unix()).
		Msg("Creating portals from history sync")
	rateLimitErrors := 0
	var wg sync.WaitGroup
	wg.Add(len(conversations))
	for i := 0; i < len(conversations); i++ {
		if ctx.Err() != nil {
			log.Warn().Err(ctx.Err()).Msg("Context cancelled, stopping history sync portal creation")
			return
		} else if wa.Client == nil {
			log.Warn().Msg("Client is nil, stopping history sync portal creation")
			return
		}
		conv := conversations[i]
		if conv.ChatJID == types.StatusBroadcastJID && !wa.Main.Config.EnableStatusBroadcast {
			wg.Done()
			continue
		} else if conv.ChatJID == types.PSAJID || conv.ChatJID == types.LegacyPSAJID {
			// We don't currently support new PSAs, so don't bother backfilling them either
			wg.Done()
			continue
		}
		// TODO can the chat info fetch be avoided entirely?
		select {
		case <-time.After(time.Duration(rateLimitErrors) * time.Second):
		case <-ctx.Done():
			log.Warn().Err(ctx.Err()).Msg("Context cancelled, stopping history sync portal creation")
			return
		}
		wrappedInfo, err := wa.getChatInfo(ctx, conv.ChatJID, conv, true)
		if errors.Is(err, whatsmeow.ErrNotInGroup) {
			log.Debug().Stringer("chat_jid", conv.ChatJID).
				Msg("Skipping creating room because the user is not a participant")
			//err = wa.Main.DB.Message.DeleteAllInChat(ctx, wa.UserLogin.ID, conv.ChatJID)
			//if err != nil {
			//	log.Err(err).Msg("Failed to delete historical messages for portal")
			//}
			err = wa.Main.DB.Conversation.Delete(ctx, wa.UserLogin.ID, conv.ChatJID)
			if err != nil {
				log.Err(err).Msg("Failed to delete conversation user is not in")
			}
			wg.Done()
			continue
		} else if errors.Is(err, whatsmeow.ErrIQRateOverLimit) {
			rateLimitErrors++
			i--
			log.Err(err).Stringer("chat_jid", conv.ChatJID).
				Int("error_count", rateLimitErrors).
				Msg("Ratelimit error getting chat info, retrying after sleep")
			select {
			case <-time.After(time.Duration(rateLimitErrors) * time.Second):
			case <-ctx.Done():
				log.Warn().Err(ctx.Err()).Msg("Context cancelled, stopping history sync portal creation")
				return
			}
			continue
		} else if err != nil {
			log.Err(err).Stringer("chat_jid", conv.ChatJID).Msg("Failed to get chat info")
			wg.Done()
			continue
		}
		res := wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.
						Stringer("chat_jid", conv.ChatJID).
						Time("latest_message_ts", conv.LastMessageTimestamp)
				},
				PortalKey:    wa.makeWAPortalKey(conv.ChatJID),
				CreatePortal: true,
				PostHandleFunc: func(ctx context.Context, portal *bridgev2.Portal) {
					err := wa.Main.DB.Conversation.MarkSynced(ctx, wa.UserLogin.ID, conv.ChatJID, loginTS)
					if err != nil {
						zerolog.Ctx(ctx).Err(err).Msg("Failed to mark conversation as bridged")
					}
					wg.Done()
				},
			},
			ChatInfo:        wrappedInfo,
			LatestMessageTS: conv.LastMessageTimestamp,
		})
		if !res.Success {
			log.Debug().Msg("Cancelling history sync portal creation loop")
			return
		}
	}
	log.Info().Int("conversation_count", len(conversations)).Msg("Finished creating portals from history sync")
	go func() {
		wg.Wait()
		wa.UserLogin.Metadata.(*waid.UserLoginMetadata).HistorySyncPortalsNeedCreating = false
		err = wa.UserLogin.Save(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save user login history sync portals created flag")
		}
		log.Info().Msg("Finished processing all history sync chat resync events")
	}()
}

func (wa *WhatsAppClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	portalJID, err := waid.ParsePortalID(params.Portal.ID)
	if err != nil {
		return nil, err
	}
	var markRead bool
	var startTime, endTime *time.Time
	var conv *wadb.Conversation
	if params.Forward || wa.Main.Config.HistorySync.BackwardsOnDemand {
		conv, err = wa.Main.DB.Conversation.Get(ctx, wa.UserLogin.ID, portalJID)
		if err != nil {
			return nil, fmt.Errorf("failed to get conversation from database: %w", err)
		}
	}
	if params.Forward {
		if params.AnchorMessage != nil {
			startTime = ptr.Ptr(params.AnchorMessage.Timestamp)
		}
		if conv != nil {
			markRead = !ptr.Val(conv.MarkedAsUnread) && ptr.Val(conv.UnreadCount) == 0
		}
	} else {
		if params.AnchorMessage != nil {
			endTime = ptr.Ptr(params.AnchorMessage.Timestamp)
		}
		if params.Cursor != "" {
			endTimeUnix, err := strconv.ParseInt(string(params.Cursor), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse cursor: %w", err)
			}
			cursorTime := time.Unix(endTimeUnix, 0)
			if endTime == nil || cursorTime.Before(*endTime) {
				endTime = &cursorTime
			}
		}
	}
	var anchorID types.MessageID
	if params.AnchorMessage != nil {
		parsedID, _ := waid.ParseMessageID(params.AnchorMessage.ID)
		if parsedID != nil {
			anchorID = parsedID.ID
		}
	}
	var hasMore bool
	if !params.Forward && wa.Main.Config.HistorySync.BackwardsOnDemand {
		hasMore = conv != nil && ptr.Val(conv.EndOfHistoryTransferType) == waHistorySync.Conversation_COMPLETE_BUT_MORE_MESSAGES_REMAIN_ON_PRIMARY
	}
	messages, err := wa.Main.DB.Message.GetBetween(ctx, wa.UserLogin.ID, portalJID, startTime, endTime, params.Count+1)
	if err != nil {
		return nil, fmt.Errorf("failed to load messages from database: %w", err)
	} else if len(messages) == 0 || (len(messages) == 1 && anchorID != "" && messages[0].GetKey().GetID() == anchorID) {
		wa.deleteHistorySyncMessages(ctx, portalJID, 0, 0)
		if hasMore && !params.AllowSlowFetch {
			return &bridgev2.FetchMessagesResponse{
				MoreRequiresSlowFetch: true,
				HasMore:               true,
				Forward:               params.Forward,
			}, nil
		} else if hasMore {
			return wa.fetchMessagesFromPhone(ctx, params)
		}
		return &bridgev2.FetchMessagesResponse{
			HasMore: false,
			Forward: params.Forward,
		}, nil
	}
	if len(messages) > params.Count {
		oldestTS := messages[len(messages)-1].GetMessageTimestamp()
		hasMore = true
		// For safety, cut off messages with the oldest timestamp in the response.
		// Otherwise, if there are multiple messages with the same timestamp, the next fetch may miss some.
		for i := len(messages) - 2; i >= 0; i-- {
			if messages[i].GetMessageTimestamp() > oldestTS {
				messages = messages[:i+1]
				break
			}
		}
	}
	resp, err := wa.convertHistorySyncMessages(ctx, params.Portal, portalJID, messages, true)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}
	resp.HasMore = hasMore
	resp.Forward = params.Forward
	resp.MarkRead = markRead
	return resp, nil
}

func (wa *WhatsAppClient) deleteHistorySyncMessages(ctx context.Context, portalJID types.JID, newestTS, oldestTS uint64) {
	var err error
	var rows int64
	if (newestTS == 0 && oldestTS == 0) || !wa.Main.Bridge.Config.Backfill.Queue.AnyEnabled() {
		// If the backfill queue isn't enabled, delete all messages after backfilling a batch.
		rows, err = wa.Main.DB.Message.DeleteAllInChat(ctx, wa.UserLogin.ID, portalJID)
	} else {
		// Otherwise just delete the messages that got backfilled
		rows, err = wa.Main.DB.Message.DeleteBetween(ctx, wa.UserLogin.ID, portalJID, newestTS, oldestTS)
	}
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).
			Stringer("portal_jid", portalJID).
			Uint64("newest_ts", newestTS).
			Uint64("oldest_ts", oldestTS).
			Msg("Failed to delete messages from database after backfill")
	} else {
		zerolog.Ctx(ctx).Debug().
			Stringer("portal_jid", portalJID).
			Uint64("newest_ts", newestTS).
			Uint64("oldest_ts", oldestTS).
			Int64("rows_affected", rows).
			Msg("Deleted history sync messages from database")
	}
}

func (wa *WhatsAppClient) convertHistorySyncMessages(
	ctx context.Context,
	portal *bridgev2.Portal,
	portalJID types.JID,
	messages []*waWeb.WebMessageInfo,
	explodeOnError bool,
) (*bridgev2.FetchMessagesResponse, error) {
	oldestTS := messages[len(messages)-1].GetMessageTimestamp()
	newestTS := messages[0].GetMessageTimestamp()
	convertedMessages := make([]*bridgev2.BackfillMessage, 0, len(messages))
	var mediaRequests []*wadb.MediaRequest
	for i, msg := range messages {
		evt, err := wa.Client.ParseWebMessage(portalJID, msg)
		if err != nil {
			if explodeOnError {
				// This should never happen because the info is already parsed once before being stored in the database
				return nil, fmt.Errorf("failed to parse info of message %s: %w", msg.GetKey().GetID(), err)
			}
			zerolog.Ctx(ctx).Warn().Err(err).
				Int("msg_index", i).
				Str("msg_id", msg.GetKey().GetID()).
				Uint64("msg_time_seconds", msg.GetMessageTimestamp()).
				Msg("Dropping historical message due to parse error")
			continue
		}
		if !explodeOnError {
			msgType := getMessageType(evt.Message)
			if msgType == "ignore" || strings.HasPrefix(msgType, "unknown_protocol_") {
				continue
			}
		}
		isViewOnce := evt.IsViewOnce || evt.IsViewOnceV2 || evt.IsViewOnceV2Extension
		converted, mediaReq := wa.convertHistorySyncMessage(
			ctx, portal, &evt.Info, evt.Message, evt.RawMessage, isViewOnce, msg.Reactions,
		)
		convertedMessages = append(convertedMessages, converted)
		if mediaReq != nil {
			mediaRequests = append(mediaRequests, mediaReq)
		}
	}
	slices.Reverse(convertedMessages)
	return &bridgev2.FetchMessagesResponse{
		Messages: convertedMessages,
		Cursor:   networkid.PaginationCursor(strconv.FormatUint(oldestTS, 10)),
		CompleteCallback: func() {
			// TODO this only deletes after backfilling. If there's no need for backfill after a relogin,
			//      the messages will be stuck in the database
			wa.deleteHistorySyncMessages(ctx, portalJID, newestTS, oldestTS)
			if len(mediaRequests) > 0 {
				go func(ctx context.Context) {
					for _, req := range mediaRequests {
						err := wa.Main.DB.MediaRequest.Put(ctx, req)
						if err != nil {
							zerolog.Ctx(ctx).Err(err).Msg("Failed to save media request to database")
						}
						if wa.Main.Config.HistorySync.MediaRequests.AutoRequestMedia && wa.Main.Config.HistorySync.MediaRequests.RequestMethod == MediaRequestMethodImmediate {
							wa.sendMediaRequest(ctx, req)
						}
					}
				}(context.WithoutCancel(ctx))
			}
		},
	}, nil
}

func (wa *WhatsAppClient) fetchMessagesFromPhone(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if params.AnchorMessage == nil {
		return nil, fmt.Errorf("anchor message is required to fetch messages from phone")
	}
	parsed, err := waid.ParseMessageID(params.AnchorMessage.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse anchor message ID: %w", err)
	}

	msgID := wa.Client.GenerateMessageID()
	reqData := wa.Client.BuildHistorySyncRequest(&types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     parsed.Chat,
			Sender:   parsed.Sender,
			IsFromMe: parsed.Sender.ToNonAD() == wa.JID.ToNonAD() || parsed.Sender.ToNonAD() == wa.Device.GetLID().ToNonAD(),
			IsGroup:  parsed.Chat.Server == types.GroupServer,
		},
		ID:        parsed.ID,
		Timestamp: params.AnchorMessage.Timestamp,
	}, 50)
	zerolog.Ctx(ctx).Debug().
		Str("request_msg_id", msgID).
		Any("anchor_msg_parsed", parsed).
		Any("request_data", reqData).
		Msg("Sending history sync request")
	_, err = wa.Client.SendMessage(ctx, wa.JID.ToNonAD(), reqData, whatsmeow.SendRequestExtra{
		ID:   msgID,
		Peer: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send history sync request: %w", err)
	}
	return &bridgev2.FetchMessagesResponse{
		HasMore: true,
		Pending: true,
	}, nil
}

func (wa *WhatsAppClient) handleOnDemandHistorySync(ctx context.Context, blob *waHistorySync.HistorySync) {
	if len(blob.GetConversations()) > 1 {
		zerolog.Ctx(ctx).Warn().
			Int("conversation_count", len(blob.GetConversations())).
			Msg("Received on-demand history sync with multiple conversations")
	}
	for _, conv := range blob.GetConversations() {
		portalJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Str("jid", conv.GetID()).Msg("Failed to parse portal JID")
			continue
		}
		portal, err := wa.Main.Bridge.GetPortalByKey(ctx, wa.makeWAPortalKey(portalJID))
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("portal_jid", portalJID).Msg("Failed to get portal for on-demand history sync")
			continue
		}
		ctx := zerolog.Ctx(ctx).With().
			Str("portal_id", string(portal.ID)).
			Str("portal_receiver", string(portal.Receiver)).
			Stringer("portal_mxid", portal.MXID).
			Logger().WithContext(ctx)
		portal.HandleRemoteBackfill(ctx, wa.UserLogin, &simplevent.Backfill{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventBackfill,
				PortalKey: portal.PortalKey,
			},
			GetDataFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.FetchMessagesResponse, error) {
				if len(conv.GetMessages()) == 0 {
					return &bridgev2.FetchMessagesResponse{}, nil
				}
				messages := make([]*waWeb.WebMessageInfo, len(conv.GetMessages()))
				for i, rawMsg := range conv.GetMessages() {
					messages[i] = rawMsg.Message
				}
				zerolog.Ctx(ctx).Debug().
					Int("message_count", len(messages)).
					Stringer("end_of_history_type", conv.GetEndOfHistoryTransferType()).
					Msg("Converting messages to bridge from on-demand history sync")
				resp, err := wa.convertHistorySyncMessages(ctx, portal, portalJID, messages, false)
				if err != nil {
					return nil, err
				}
				resp.HasMore = conv.GetEndOfHistoryTransferType() == waHistorySync.Conversation_COMPLETE_BUT_MORE_MESSAGES_REMAIN_ON_PRIMARY
				return resp, nil
			},
		})
	}
}

func (wa *WhatsAppClient) convertHistorySyncMessage(
	ctx context.Context, portal *bridgev2.Portal, info *types.MessageInfo, msg, rawMsg *waE2E.Message, isViewOnce bool, reactions []*waWeb.Reaction,
) (*bridgev2.BackfillMessage, *wadb.MediaRequest) {
	// New messages turn these into edits, but in backfill we only have the last version,
	// so no need to do the edit thing. Instead, just unwrap the message.
	if msg.GetAssociatedChildMessage().GetMessage() != nil {
		msg = msg.GetAssociatedChildMessage().GetMessage()
	}
	// TODO use proper intent
	intent := wa.Main.Bridge.Bot
	wrapped := &bridgev2.BackfillMessage{
		ConvertedMessage: wa.Main.MsgConv.ToMatrix(ctx, portal, wa.Client, intent, msg, rawMsg, info, isViewOnce, true, nil),
		Sender:           wa.makeEventSender(ctx, info.Sender),
		ID:               waid.MakeMessageID(info.Chat, info.Sender, info.ID),
		TxnID:            networkid.TransactionID(waid.MakeMessageID(info.Chat, info.Sender, info.ID)),
		Timestamp:        info.Timestamp,
		StreamOrder:      info.Timestamp.Unix(),
		Reactions:        make([]*bridgev2.BackfillReaction, 0, len(reactions)),
	}
	mediaReq := wa.processFailedMedia(ctx, portal.PortalKey, wrapped.ID, wrapped.ConvertedMessage, true)
	for _, reaction := range reactions {
		var sender types.JID
		if reaction.GetKey().GetFromMe() {
			sender = wa.JID
		} else if reaction.GetKey().GetParticipant() != "" {
			sender, _ = types.ParseJID(*reaction.Key.Participant)
		} else if info.Chat.Server == types.DefaultUserServer || info.Chat.Server == types.BotServer {
			sender = info.Chat
		}
		if sender.IsEmpty() {
			continue
		}
		wrapped.Reactions = append(wrapped.Reactions, &bridgev2.BackfillReaction{
			TargetPart: ptr.Ptr(networkid.PartID("")),
			Timestamp:  time.UnixMilli(reaction.GetSenderTimestampMS()),
			Sender:     wa.makeEventSender(ctx, sender),
			Emoji:      reaction.GetText(),
		})
	}
	return wrapped, mediaReq
}
