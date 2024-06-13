// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"go.mau.fi/util/variationselector"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
)

// region User history sync handling

type wrappedInfo struct {
	*types.MessageInfo
	Type  database.MessageType
	Error database.MessageErrorType

	SenderMXID id.UserID

	ReactionTarget types.MessageID

	MediaKey []byte

	ExpirationStart time.Time
	ExpiresIn       time.Duration
}

func (user *User) handleHistorySyncsLoop() {
	if !user.bridge.Config.Bridge.HistorySync.Backfill {
		return
	}

	batchSend := user.bridge.SpecVersions.Supports(mautrix.BeeperFeatureBatchSending)
	if batchSend {
		// Start the backfill queue.
		user.BackfillQueue = &BackfillQueue{
			BackfillQuery:   user.bridge.DB.BackfillQueue,
			reCheckChannels: []chan bool{},
		}

		forwardAndImmediate := []database.BackfillType{database.BackfillImmediate, database.BackfillForward}

		// Immediate backfills can be done in parallel
		for i := 0; i < user.bridge.Config.Bridge.HistorySync.Immediate.WorkerCount; i++ {
			go user.HandleBackfillRequestsLoop(forwardAndImmediate, []database.BackfillType{})
		}

		// Deferred backfills should be handled synchronously so as not to
		// overload the homeserver. Users can configure their backfill stages
		// to be more or less aggressive with backfilling at this stage.
		go user.HandleBackfillRequestsLoop([]database.BackfillType{database.BackfillDeferred}, forwardAndImmediate)
	}

	if user.bridge.Config.Bridge.HistorySync.MediaRequests.AutoRequestMedia &&
		user.bridge.Config.Bridge.HistorySync.MediaRequests.RequestMethod == config.MediaRequestMethodLocalTime {
		go user.dailyMediaRequestLoop()
	}

	// Always save the history syncs for the user. If they want to enable
	// backfilling in the future, we will have it in the database.
	for {
		select {
		case evt := <-user.historySyncs:
			if evt == nil {
				return
			}
			user.storeHistorySync(evt.Data)
		case <-user.enqueueBackfillsTimer.C:
			if batchSend {
				user.enqueueAllBackfills()
			} else {
				user.backfillAll()
			}
		}
	}
}

const EnqueueBackfillsDelay = 30 * time.Second

func (user *User) enqueueAllBackfills() {
	log := user.zlog.With().
		Str("method", "User.enqueueAllBackfills").
		Logger()
	ctx := log.WithContext(context.TODO())
	nMostRecent, err := user.bridge.DB.HistorySync.GetRecentConversations(ctx, user.MXID, user.bridge.Config.Bridge.HistorySync.MaxInitialConversations)
	if err != nil {
		log.Err(err).Msg("Failed to get recent history sync conversations from database")
		return
	} else if len(nMostRecent) == 0 {
		return
	}
	log.Info().
		Int("chat_count", len(nMostRecent)).
		Msg("Enqueueing backfills for recent chats in history sync")
	// Find the portals for all the conversations.
	portals := make([]*Portal, 0, len(nMostRecent))
	for _, conv := range nMostRecent {
		jid, err := types.ParseJID(conv.ConversationID)
		if err != nil {
			log.Err(err).Str("conversation_id", conv.ConversationID).Msg("Failed to parse chat JID in history sync")
			continue
		}
		portals = append(portals, user.GetPortalByJID(jid))
	}

	user.EnqueueImmediateBackfills(ctx, portals)
	user.EnqueueForwardBackfills(ctx, portals)
	user.EnqueueDeferredBackfills(ctx, portals)

	// Tell the queue to check for new backfill requests.
	user.BackfillQueue.ReCheck()
}

func (user *User) backfillAll() {
	log := user.zlog.With().
		Str("method", "User.backfillAll").
		Logger()
	ctx := log.WithContext(context.TODO())
	conversations, err := user.bridge.DB.HistorySync.GetRecentConversations(ctx, user.MXID, -1)
	if err != nil {
		log.Err(err).Msg("Failed to get history sync conversations from database")
		return
	} else if len(conversations) == 0 {
		return
	}
	log.Info().
		Int("conversation_count", len(conversations)).
		Msg("Probably received all history sync blobs, now backfilling conversations")
	limit := user.bridge.Config.Bridge.HistorySync.MaxInitialConversations
	bridgedCount := 0
	// Find the portals for all the conversations.
	for _, conv := range conversations {
		jid, err := types.ParseJID(conv.ConversationID)
		if err != nil {
			log.Err(err).
				Str("conversation_id", conv.ConversationID).
				Msg("Failed to parse chat JID in history sync")
			continue
		}
		portal := user.GetPortalByJID(jid)
		if portal.MXID != "" {
			log.Debug().
				Str("portal_jid", portal.Key.JID.String()).
				Msg("Chat already has a room, deleting messages from database")
			err = user.bridge.DB.HistorySync.DeleteConversation(ctx, user.MXID, portal.Key.JID.String())
			if err != nil {
				log.Err(err).Str("portal_jid", portal.Key.JID.String()).
					Msg("Failed to delete history sync conversation with existing portal from database")
			}
			bridgedCount++
		} else if hasMessages, err := user.bridge.DB.HistorySync.ConversationHasMessages(ctx, user.MXID, portal.Key); err != nil {
			log.Err(err).Str("portal_jid", portal.Key.JID.String()).Msg("Failed to check if chat has messages in history sync")
		} else if !hasMessages {
			log.Debug().Str("portal_jid", portal.Key.JID.String()).Msg("Skipping chat with no messages in history sync")
			err = user.bridge.DB.HistorySync.DeleteConversation(ctx, user.MXID, portal.Key.JID.String())
			if err != nil {
				log.Err(err).Str("portal_jid", portal.Key.JID.String()).
					Msg("Failed to delete history sync conversation with no messages from database")
			}
		} else if limit < 0 || bridgedCount < limit {
			bridgedCount++
			err = portal.CreateMatrixRoom(ctx, user, nil, nil, true, true)
			if err != nil {
				log.Err(err).Msg("Failed to create Matrix room for backfill")
			}
		}
	}
}

func (portal *Portal) legacyBackfill(ctx context.Context, user *User) {
	defer portal.latestEventBackfillLock.Unlock()
	// This should only be called from CreateMatrixRoom which locks latestEventBackfillLock before creating the room.
	if portal.latestEventBackfillLock.TryLock() {
		panic("legacyBackfill() called without locking latestEventBackfillLock")
	}
	log := zerolog.Ctx(ctx).With().Str("action", "legacy backfill").Logger()
	ctx = log.WithContext(ctx)
	conv, err := user.bridge.DB.HistorySync.GetConversation(ctx, user.MXID, portal.Key)
	if err != nil {
		log.Err(err).Msg("Failed to get history sync conversation data for backfill")
		return
	}
	messages, err := user.bridge.DB.HistorySync.GetMessagesBetween(ctx, user.MXID, portal.Key.JID.String(), nil, nil, portal.bridge.Config.Bridge.HistorySync.MessageCount)
	if err != nil {
		log.Err(err).Msg("Failed to get history sync messages for backfill")
		return
	}
	log.Debug().Int("message_count", len(messages)).Msg("Got messages to backfill from database")
	for i := len(messages) - 1; i >= 0; i-- {
		msgEvt, err := user.Client.ParseWebMessage(portal.Key.JID, messages[i])
		if err != nil {
			log.Warn().Err(err).
				Int("msg_index", i).
				Str("msg_id", messages[i].GetKey().GetId()).
				Uint64("msg_time_seconds", messages[i].GetMessageTimestamp()).
				Msg("Dropping historical message due to parse error")
			continue
		}
		ctx := log.With().
			Str("message_id", msgEvt.Info.ID).
			Stringer("message_sender", msgEvt.Info.Sender).
			Logger().
			WithContext(ctx)
		portal.handleMessage(ctx, user, msgEvt, true)
	}
	if conv != nil {
		isUnread := conv.MarkedAsUnread || conv.UnreadCount > 0
		isTooOld := user.bridge.Config.Bridge.HistorySync.UnreadHoursThreshold > 0 && conv.LastMessageTimestamp.Before(time.Now().Add(time.Duration(-user.bridge.Config.Bridge.HistorySync.UnreadHoursThreshold)*time.Hour))
		shouldMarkAsRead := !isUnread || isTooOld
		if shouldMarkAsRead {
			user.markSelfReadFull(ctx, portal)
		}
	}
	log.Info().Msg("Backfill complete, deleting leftover messages from database")
	err = user.bridge.DB.HistorySync.DeleteConversation(ctx, user.MXID, portal.Key.JID.String())
	if err != nil {
		log.Err(err).Msg("Failed to delete history sync conversation from database after backfill")
	}
}

func (user *User) dailyMediaRequestLoop() {
	log := user.zlog.With().
		Str("action", "daily media request loop").
		Logger()
	ctx := log.WithContext(context.Background())

	// Calculate when to do the first set of media retry requests
	now := time.Now()
	userTz, err := time.LoadLocation(user.Timezone)
	tzIsInvalid := err != nil && user.Timezone != ""
	var requestStartTime time.Time
	if tzIsInvalid {
		requestStartTime = now.Add(8 * time.Hour)
		log.Warn().Msg("Invalid time zone, using static 8 hour start time")
	} else {
		if userTz == nil {
			userTz = now.Local().Location()
		}
		tonightMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, userTz)
		midnightOffset := time.Duration(user.bridge.Config.Bridge.HistorySync.MediaRequests.RequestLocalTime) * time.Minute
		requestStartTime = tonightMidnight.Add(midnightOffset)
		// If the request time for today has already happened, we need to start the
		// request loop tomorrow instead.
		if requestStartTime.Before(now) {
			requestStartTime = requestStartTime.AddDate(0, 0, 1)
		}
	}

	// Wait to start the loop
	log.Info().Time("start_loop_at", requestStartTime).Msg("Waiting until start time to do media retry requests")
	time.Sleep(time.Until(requestStartTime))

	for {
		mediaBackfillRequests, err := user.bridge.DB.MediaBackfillRequest.GetMediaBackfillRequestsForUser(ctx, user.MXID)
		if err != nil {
			log.Err(err).Msg("Failed to get media retry requests")
		} else if len(mediaBackfillRequests) > 0 {
			log.Info().Int("media_request_count", len(mediaBackfillRequests)).Msg("Sending media retry requests")

			// Send all the media backfill requests for the user at once
			for _, req := range mediaBackfillRequests {
				portal := user.GetPortalByJID(req.PortalKey.JID)
				_, err = portal.requestMediaRetry(ctx, user, req.EventID, req.MediaKey)
				if err != nil {
					log.Err(err).
						Stringer("portal_key", req.PortalKey).
						Stringer("event_id", req.EventID).
						Msg("Failed to send media retry request")
					req.Status = database.MediaBackfillRequestStatusRequestFailed
					req.Error = err.Error()
				} else {
					log.Debug().
						Stringer("portal_key", req.PortalKey).
						Stringer("event_id", req.EventID).
						Msg("Sent media retry request")
					req.Status = database.MediaBackfillRequestStatusRequested
				}
				req.MediaKey = nil
				err = req.Upsert(ctx)
				if err != nil {
					log.Err(err).
						Stringer("portal_key", req.PortalKey).
						Stringer("event_id", req.EventID).
						Msg("Failed to save status of media retry request")
				}
			}
		}

		// Wait for 24 hours before making requests again
		time.Sleep(24 * time.Hour)
	}
}

func (user *User) backfillInChunks(ctx context.Context, req *database.BackfillTask, conv *database.HistorySyncConversation, portal *Portal) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()
	log := zerolog.Ctx(ctx)

	if len(portal.MXID) > 0 && !user.bridge.AS.StateStore.IsInRoom(ctx, portal.MXID, user.MXID) {
		portal.ensureUserInvited(ctx, user)
	}

	backfillState, err := user.bridge.DB.BackfillState.GetBackfillState(ctx, user.MXID, portal.Key)
	if backfillState == nil {
		backfillState = user.bridge.DB.BackfillState.NewBackfillState(user.MXID, portal.Key)
	}
	err = backfillState.SetProcessingBatch(ctx, true)
	if err != nil {
		log.Err(err).Msg("Failed to mark batch as being processed")
	}
	defer func() {
		err = backfillState.SetProcessingBatch(ctx, false)
		if err != nil {
			log.Err(err).Msg("Failed to mark batch as no longer being processed")
		}
	}()

	var timeEnd *time.Time
	var forward, shouldMarkAsRead bool
	portal.latestEventBackfillLock.Lock()
	if req.BackfillType == database.BackfillForward {
		// TODO this overrides the TimeStart set when enqueuing the backfill
		//      maybe the enqueue should instead include the prev event ID
		lastMessage, err := portal.bridge.DB.Message.GetLastInChat(ctx, portal.Key)
		if err != nil {
			log.Err(err).Msg("Failed to get newest message in chat")
			return
		}
		start := lastMessage.Timestamp.Add(1 * time.Second)
		req.TimeStart = &start
		// Sending events at the end of the room (= latest events)
		forward = true
	} else {
		firstMessage, err := portal.bridge.DB.Message.GetFirstInChat(ctx, portal.Key)
		if err != nil {
			log.Err(err).Msg("Failed to get oldest message in chat")
			return
		}
		if firstMessage != nil {
			end := firstMessage.Timestamp.Add(-1 * time.Second)
			timeEnd = &end
			log.Debug().
				Time("oldest_message_ts", firstMessage.Timestamp).
				Msg("Limiting backfill to messages older than oldest message")
		} else {
			// Portal is empty -> events are latest
			forward = true
		}
	}
	if !forward {
		// We'll use normal batch sending, so no need to keep blocking new message processing
		portal.latestEventBackfillLock.Unlock()
	} else {
		// This might involve sending events at the end of the room as non-historical events,
		// make sure we don't process messages until this is done.
		defer portal.latestEventBackfillLock.Unlock()

		isUnread := conv.MarkedAsUnread || conv.UnreadCount > 0
		isTooOld := user.bridge.Config.Bridge.HistorySync.UnreadHoursThreshold > 0 && conv.LastMessageTimestamp.Before(time.Now().Add(time.Duration(-user.bridge.Config.Bridge.HistorySync.UnreadHoursThreshold)*time.Hour))
		shouldMarkAsRead = !isUnread || isTooOld
	}
	allMsgs, err := user.bridge.DB.HistorySync.GetMessagesBetween(ctx, user.MXID, conv.ConversationID, req.TimeStart, timeEnd, req.MaxTotalEvents)

	sendDisappearedNotice := false
	// If expired messages are on, and a notice has not been sent to this chat
	// about it having disappeared messages at the conversation timestamp, send
	// a notice indicating so.
	if len(allMsgs) == 0 && conv.EphemeralExpiration != nil && *conv.EphemeralExpiration > 0 {
		lastMessage, err := portal.bridge.DB.Message.GetLastInChat(ctx, portal.Key)
		if err != nil {
			log.Err(err).Msg("Failed to get last message in chat to check if disappeared notice should be sent")
		}
		if lastMessage == nil || conv.LastMessageTimestamp.After(lastMessage.Timestamp) {
			sendDisappearedNotice = true
		}
	}

	if !sendDisappearedNotice && len(allMsgs) == 0 {
		log.Debug().Msg("Not backfilling chat: no bridgeable messages found")
		return
	}

	if len(portal.MXID) == 0 {
		log.Debug().Msg("Creating portal for chat as part of history sync handling")
		err = portal.CreateMatrixRoom(ctx, user, nil, nil, true, false)
		if err != nil {
			log.Err(err).Msg("Failed to create room for chat during backfill")
			return
		}
	}

	// Update the backfill status here after the room has been created.
	portal.updateBackfillStatus(ctx, backfillState)

	if sendDisappearedNotice {
		log.Debug().Time("last_message_time", conv.LastMessageTimestamp).
			Msg("Sending notice that there are disappeared messages in the chat")
		resp, err := portal.sendMessage(ctx, portal.MainIntent(), event.EventMessage, &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    portal.formatDisappearingMessageNotice(),
		}, nil, conv.LastMessageTimestamp.UnixMilli())
		if err != nil {
			log.Err(err).Msg("Failed to send disappeared messages notice event")
			return
		}

		msg := portal.bridge.DB.Message.New()
		msg.Chat = portal.Key
		msg.MXID = resp.EventID
		msg.JID = types.MessageID(resp.EventID)
		msg.Timestamp = conv.LastMessageTimestamp
		msg.SenderMXID = portal.MainIntent().UserID
		msg.Sent = true
		msg.Type = database.MsgFake
		err = msg.Insert(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save fake message entry for disappearing message timer in backfill")
		}
		user.markSelfReadFull(ctx, portal)
		return
	}

	log.Info().
		Int("message_count", len(allMsgs)).
		Int("max_batch_events", req.MaxBatchEvents).
		Msg("Backfilling messages")
	toBackfill := allMsgs[0:]
	for len(toBackfill) > 0 {
		var msgs []*waProto.WebMessageInfo
		if len(toBackfill) <= req.MaxBatchEvents || req.MaxBatchEvents < 0 {
			msgs = toBackfill
			toBackfill = nil
		} else {
			msgs = toBackfill[:req.MaxBatchEvents]
			toBackfill = toBackfill[req.MaxBatchEvents:]
		}

		if len(msgs) > 0 {
			time.Sleep(time.Duration(req.BatchDelay) * time.Second)
			log.Debug().Int("batch_message_count", len(msgs)).Msg("Backfilling message batch")
			portal.backfill(ctx, user, msgs, forward, shouldMarkAsRead)
		}
	}
	log.Debug().Int("message_count", len(allMsgs)).Msg("Finished backfilling messages in queue entry")
	err = user.bridge.DB.HistorySync.DeleteMessages(ctx, user.MXID, conv.ConversationID, allMsgs)
	if err != nil {
		log.Err(err).Msg("Failed to delete history sync messages after backfilling")
	}

	if req.TimeStart == nil {
		// If the time start is nil, then there's no more history to backfill.
		backfillState.BackfillComplete = true

		if conv.EndOfHistoryTransferType == waProto.Conversation_COMPLETE_BUT_MORE_MESSAGES_REMAIN_ON_PRIMARY {
			// Since there are more messages on the phone, but we can't
			// backfill any more of them, indicate that the last timestamp
			// that we expect to be backfilled is the oldest one that was just
			// backfilled.
			backfillState.FirstExpectedTimestamp = allMsgs[len(allMsgs)-1].GetMessageTimestamp()
		} else if conv.EndOfHistoryTransferType == waProto.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY {
			// Since there are no more messages left on the phone, we've
			// backfilled everything. Indicate so by setting the expected
			// timestamp to 0 which means that the backfill goes to the
			// beginning of time.
			backfillState.FirstExpectedTimestamp = 0
		}
		err = backfillState.Upsert(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to mark backfill state as completed in database")
		}
		portal.updateBackfillStatus(ctx, backfillState)
	}
}

func (user *User) storeHistorySync(evt *waProto.HistorySync) {
	if evt == nil || evt.SyncType == nil {
		return
	}
	log := user.zlog.With().
		Str("method", "User.storeHistorySync").
		Str("sync_type", evt.GetSyncType().String()).
		Uint32("chunk_order", evt.GetChunkOrder()).
		Uint32("progress", evt.GetProgress()).
		Logger()
	ctx := log.WithContext(context.TODO())
	if evt.GetGlobalSettings() != nil {
		log.Debug().Interface("global_settings", evt.GetGlobalSettings()).Msg("Got global settings in history sync")
	}
	if evt.GetSyncType() == waProto.HistorySync_INITIAL_STATUS_V3 || evt.GetSyncType() == waProto.HistorySync_PUSH_NAME || evt.GetSyncType() == waProto.HistorySync_NON_BLOCKING_DATA {
		log.Debug().
			Int("conversation_count", len(evt.GetConversations())).
			Int("pushname_count", len(evt.GetPushnames())).
			Int("status_count", len(evt.GetStatusV3Messages())).
			Int("recent_sticker_count", len(evt.GetRecentStickers())).
			Int("past_participant_count", len(evt.GetPastParticipants())).
			Msg("Ignoring history sync")
		return
	}
	log.Info().
		Int("conversation_count", len(evt.GetConversations())).
		Int("past_participant_count", len(evt.GetPastParticipants())).
		Msg("Storing history sync")

	successfullySavedTotal := 0
	failedToSaveTotal := 0
	totalMessageCount := 0
	for _, conv := range evt.GetConversations() {
		jid, err := types.ParseJID(conv.GetId())
		if err != nil {
			totalMessageCount += len(conv.GetMessages())
			log.Warn().Err(err).
				Str("chat_jid", conv.GetId()).
				Int("msg_count", len(conv.GetMessages())).
				Msg("Failed to parse chat JID in history sync")
			continue
		} else if jid.Server == types.BroadcastServer {
			log.Debug().Str("chat_jid", jid.String()).Msg("Skipping broadcast list in history sync")
			continue
		} else if jid.Server == types.HiddenUserServer {
			log.Debug().Str("chat_jid", jid.String()).Msg("Skipping hidden user JID chat in history sync")
			continue
		}
		totalMessageCount += len(conv.GetMessages())
		log := log.With().
			Str("chat_jid", jid.String()).
			Int("msg_count", len(conv.GetMessages())).
			Logger()

		var portal *Portal
		initPortal := func() {
			if portal != nil {
				return
			}
			portal = user.GetPortalByJID(jid)
			historySyncConversation := user.bridge.DB.HistorySync.NewConversationWithValues(
				user.MXID,
				conv.GetId(),
				portal.Key,
				getConversationTimestamp(conv),
				conv.GetMuteEndTime(),
				conv.GetArchived(),
				conv.GetPinned(),
				conv.GetDisappearingMode().GetInitiator(),
				conv.GetEndOfHistoryTransferType(),
				conv.EphemeralExpiration,
				conv.GetMarkedAsUnread(),
				conv.GetUnreadCount())
			err := historySyncConversation.Upsert(ctx)
			if err != nil {
				log.Err(err).Msg("Failed to insert history sync conversation into database")
			}
		}

		var minTime, maxTime time.Time
		var minTimeIndex, maxTimeIndex int

		successfullySaved := 0
		failedToSave := 0
		unsupportedTypes := 0
		for i, rawMsg := range conv.GetMessages() {
			// Don't store messages that will just be skipped.
			msgEvt, err := user.Client.ParseWebMessage(jid, rawMsg.GetMessage())
			if err != nil {
				log.Warn().Err(err).
					Int("msg_index", i).
					Str("msg_id", rawMsg.GetMessage().GetKey().GetId()).
					Uint64("msg_time_seconds", rawMsg.GetMessage().GetMessageTimestamp()).
					Msg("Dropping historical message due to parse error")
				continue
			}
			if minTime.IsZero() || msgEvt.Info.Timestamp.Before(minTime) {
				minTime = msgEvt.Info.Timestamp
				minTimeIndex = i
			}
			if maxTime.IsZero() || msgEvt.Info.Timestamp.After(maxTime) {
				maxTime = msgEvt.Info.Timestamp
				maxTimeIndex = i
			}

			msgType := getMessageType(msgEvt.Message)
			if msgType == "unknown" || msgType == "ignore" || strings.HasPrefix(msgType, "unknown_protocol_") || !containsSupportedMessage(msgEvt.Message) {
				unsupportedTypes++
				continue
			}

			initPortal()

			message, err := user.bridge.DB.HistorySync.NewMessageWithValues(user.MXID, conv.GetId(), msgEvt.Info.ID, rawMsg)
			if err != nil {
				log.Error().Err(err).
					Int("msg_index", i).
					Str("msg_id", msgEvt.Info.ID).
					Time("msg_time", msgEvt.Info.Timestamp).
					Msg("Failed to save historical message")
				failedToSave++
				continue
			}
			err = message.Insert(ctx)
			if err != nil {
				log.Error().Err(err).
					Int("msg_index", i).
					Str("msg_id", msgEvt.Info.ID).
					Time("msg_time", msgEvt.Info.Timestamp).
					Msg("Failed to save historical message")
				failedToSave++
			} else {
				successfullySaved++
			}
		}
		successfullySavedTotal += successfullySaved
		failedToSaveTotal += failedToSave
		log.Debug().
			Int("saved_count", successfullySaved).
			Int("failed_count", failedToSave).
			Int("unsupported_msg_type_count", unsupportedTypes).
			Time("lowest_time", minTime).
			Int("lowest_time_index", minTimeIndex).
			Time("highest_time", maxTime).
			Int("highest_time_index", maxTimeIndex).
			Dict("metadata", zerolog.Dict().
				Uint32("ephemeral_expiration", conv.GetEphemeralExpiration()).
				Bool("marked_unread", conv.GetMarkedAsUnread()).
				Bool("archived", conv.GetArchived()).
				Uint32("pinned", conv.GetPinned()).
				Uint64("mute_end", conv.GetMuteEndTime()).
				Uint32("unread_count", conv.GetUnreadCount()),
			).
			Msg("Saved messages from history sync conversation")
	}
	log.Info().
		Int("total_saved_count", successfullySavedTotal).
		Int("total_failed_count", failedToSaveTotal).
		Int("total_message_count", totalMessageCount).
		Msg("Finished storing history sync")

	// If this was the initial bootstrap, enqueue immediate backfills for the
	// most recent portals. If it's the last history sync event, start
	// backfilling the rest of the history of the portals.
	if user.bridge.Config.Bridge.HistorySync.Backfill {
		user.enqueueBackfillsTimer.Reset(EnqueueBackfillsDelay)
	}
}

func getConversationTimestamp(conv *waProto.Conversation) uint64 {
	convTs := conv.GetConversationTimestamp()
	if convTs == 0 && len(conv.GetMessages()) > 0 {
		convTs = conv.Messages[0].GetMessage().GetMessageTimestamp()
	}
	return convTs
}

func (user *User) EnqueueImmediateBackfills(ctx context.Context, portals []*Portal) {
	for priority, portal := range portals {
		maxMessages := user.bridge.Config.Bridge.HistorySync.Immediate.MaxEvents
		initialBackfill := user.bridge.DB.BackfillQueue.NewWithValues(user.MXID, database.BackfillImmediate, priority, portal.Key, nil, maxMessages, maxMessages, 0)
		err := initialBackfill.Insert(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("portal_key", portal.Key).
				Msg("Failed to insert immediate backfill into database")
		}
	}
}

func (user *User) EnqueueDeferredBackfills(ctx context.Context, portals []*Portal) {
	numPortals := len(portals)
	for stageIdx, backfillStage := range user.bridge.Config.Bridge.HistorySync.Deferred {
		for portalIdx, portal := range portals {
			var startDate *time.Time = nil
			if backfillStage.StartDaysAgo > 0 {
				startDaysAgo := time.Now().AddDate(0, 0, -backfillStage.StartDaysAgo)
				startDate = &startDaysAgo
			}
			backfillMessages := user.bridge.DB.BackfillQueue.NewWithValues(
				user.MXID, database.BackfillDeferred, stageIdx*numPortals+portalIdx, portal.Key, startDate, backfillStage.MaxBatchEvents, -1, backfillStage.BatchDelay)
			err := backfillMessages.Insert(ctx)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).
					Stringer("portal_key", portal.Key).
					Msg("Failed to insert deferred backfill into database")
			}
		}
	}
}

func (user *User) EnqueueForwardBackfills(ctx context.Context, portals []*Portal) {
	for priority, portal := range portals {
		lastMsg, err := user.bridge.DB.Message.GetLastInChat(ctx, portal.Key)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("portal_key", portal.Key).
				Msg("Failed to get last message in chat to enqueue forward backfill")
		} else if lastMsg == nil {
			continue
		}
		backfill := user.bridge.DB.BackfillQueue.NewWithValues(
			user.MXID, database.BackfillForward, priority, portal.Key, &lastMsg.Timestamp, -1, -1, 0)
		err = backfill.Insert(ctx)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).
				Stringer("portal_key", portal.Key).
				Msg("Failed to insert forward backfill into database")
		}
	}
}

// endregion
// region Portal backfilling

func (portal *Portal) deterministicEventID(sender types.JID, messageID types.MessageID, partName string) id.EventID {
	data := fmt.Sprintf("%s/whatsapp/%s/%s", portal.MXID, sender.User, messageID)
	if partName != "" {
		data += "/" + partName
	}
	sum := sha256.Sum256([]byte(data))
	return id.EventID(fmt.Sprintf("$%s:whatsapp.com", base64.RawURLEncoding.EncodeToString(sum[:])))
}

var (
	BackfillStatusEvent = event.Type{Type: "com.beeper.backfill_status", Class: event.StateEventType}
)

func (portal *Portal) backfill(ctx context.Context, source *User, messages []*waProto.WebMessageInfo, isForward, atomicMarkAsRead bool) {
	log := zerolog.Ctx(ctx)
	var req mautrix.ReqBeeperBatchSend
	var infos []*wrappedInfo

	req.Forward = isForward
	if atomicMarkAsRead {
		req.MarkReadBy = source.MXID
	}

	log.Info().
		Bool("forward", isForward).
		Int("message_count", len(messages)).
		Msg("Processing history sync message batch")
	// The messages are ordered newest to oldest, so iterate them in reverse order.
	for i := len(messages) - 1; i >= 0; i-- {
		webMsg := messages[i]
		msgEvt, err := source.Client.ParseWebMessage(portal.Key.JID, webMsg)
		if err != nil {
			continue
		}
		log := log.With().
			Str("message_id", msgEvt.Info.ID).
			Stringer("message_sender", msgEvt.Info.Sender).
			Logger()
		ctx := log.WithContext(ctx)

		msgType := getMessageType(msgEvt.Message)
		if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
			if msgType != "ignore" {
				log.Debug().Msg("Skipping message with unknown type in backfill")
			}
			continue
		}
		if webMsg.GetPushName() != "" && webMsg.GetPushName() != "-" {
			existingContact, _ := source.Client.Store.Contacts.GetContact(msgEvt.Info.Sender)
			if !existingContact.Found || existingContact.PushName == "" {
				changed, _, err := source.Client.Store.Contacts.PutPushName(msgEvt.Info.Sender, webMsg.GetPushName())
				if err != nil {
					log.Err(err).Msg("Failed to save push name from historical message to device store")
				} else if changed {
					log.Debug().Str("push_name", webMsg.GetPushName()).Msg("Got push name from historical message")
				}
			}
		}
		puppet := portal.getMessagePuppet(ctx, source, &msgEvt.Info)
		if puppet == nil {
			continue
		}

		converted := portal.convertMessage(ctx, puppet.IntentFor(portal), source, &msgEvt.Info, msgEvt.Message, true)
		if converted == nil {
			log.Debug().Msg("Skipping unsupported message in backfill")
			continue
		}
		if converted.ReplyTo != nil {
			portal.SetReply(ctx, converted.Content, converted.ReplyTo, true)
		}
		err = portal.appendBatchEvents(ctx, source, converted, &msgEvt.Info, webMsg, &req.Events, &infos)
		if err != nil {
			log.Err(err).Msg("Failed to handle message in backfill")
		}
	}
	log.Info().Int("event_count", len(req.Events)).Msg("Made Matrix events from messages in batch")

	if len(req.Events) == 0 {
		return
	}

	resp, err := portal.MainIntent().BeeperBatchSend(ctx, portal.MXID, &req)
	if err != nil {
		log.Err(err).Msg("Failed to send batch of messages")
		return
	}
	err = portal.bridge.DB.DoTxn(ctx, nil, func(ctx context.Context) error {
		return portal.finishBatch(ctx, resp.EventIDs, infos)
	})
	if err != nil {
		log.Err(err).Msg("Failed to save message batch to database")
		return
	}
	log.Info().Msg("Successfully sent backfill batch")
	if portal.bridge.Config.Bridge.HistorySync.MediaRequests.AutoRequestMedia {
		go portal.requestMediaRetries(context.TODO(), source, resp.EventIDs, infos)
	}
}

func (portal *Portal) requestMediaRetries(ctx context.Context, source *User, eventIDs []id.EventID, infos []*wrappedInfo) {
	for i, info := range infos {
		if info != nil && info.Error == database.MsgErrMediaNotFound && info.MediaKey != nil {
			switch portal.bridge.Config.Bridge.HistorySync.MediaRequests.RequestMethod {
			case config.MediaRequestMethodImmediate:
				err := source.Client.SendMediaRetryReceipt(info.MessageInfo, info.MediaKey)
				if err != nil {
					portal.zlog.Err(err).Str("message_id", info.ID).Msg("Failed to send post-backfill media retry request")
				} else {
					portal.zlog.Debug().Str("message_id", info.ID).Msg("Sent post-backfill media retry request")
				}
			case config.MediaRequestMethodLocalTime:
				req := portal.bridge.DB.MediaBackfillRequest.NewMediaBackfillRequestWithValues(source.MXID, portal.Key, eventIDs[i], info.MediaKey)
				err := req.Upsert(ctx)
				if err != nil {
					portal.zlog.Err(err).
						Stringer("event_id", eventIDs[i]).
						Msg("Failed to upsert media backfill request")
				}
			}
		}
	}
}

func (portal *Portal) appendBatchEvents(ctx context.Context, source *User, converted *ConvertedMessage, info *types.MessageInfo, raw *waProto.WebMessageInfo, eventsArray *[]*event.Event, infoArray *[]*wrappedInfo) error {
	if portal.bridge.Config.Bridge.CaptionInMessage {
		converted.MergeCaption()
	}
	mainEvt, err := portal.wrapBatchEvent(ctx, info, converted.Intent, converted.Type, converted.Content, converted.Extra, "")
	if err != nil {
		return err
	}
	expirationStart := info.Timestamp
	if raw.GetEphemeralStartTimestamp() > 0 {
		expirationStart = time.Unix(int64(raw.GetEphemeralStartTimestamp()), 0)
	}
	mainInfo := &wrappedInfo{
		MessageInfo:     info,
		Type:            database.MsgNormal,
		SenderMXID:      mainEvt.Sender,
		Error:           converted.Error,
		MediaKey:        converted.MediaKey,
		ExpirationStart: expirationStart,
		ExpiresIn:       converted.ExpiresIn,
	}
	if converted.Caption != nil {
		captionEvt, err := portal.wrapBatchEvent(ctx, info, converted.Intent, converted.Type, converted.Caption, nil, "caption")
		if err != nil {
			return err
		}
		*eventsArray = append(*eventsArray, mainEvt, captionEvt)
		*infoArray = append(*infoArray, mainInfo, nil)
	} else {
		*eventsArray = append(*eventsArray, mainEvt)
		*infoArray = append(*infoArray, mainInfo)
	}
	if converted.MultiEvent != nil {
		for i, subEvtContent := range converted.MultiEvent {
			subEvt, err := portal.wrapBatchEvent(ctx, info, converted.Intent, converted.Type, subEvtContent, nil, fmt.Sprintf("multi-%d", i))
			if err != nil {
				return err
			}
			*eventsArray = append(*eventsArray, subEvt)
			*infoArray = append(*infoArray, nil)
		}
	}
	for _, reaction := range raw.GetReactions() {
		reactionEvent, reactionInfo := portal.wrapBatchReaction(ctx, source, reaction, mainEvt.ID, info.Timestamp)
		if reactionEvent != nil {
			*eventsArray = append(*eventsArray, reactionEvent)
			*infoArray = append(*infoArray, &wrappedInfo{
				MessageInfo:    reactionInfo,
				SenderMXID:     reactionEvent.Sender,
				ReactionTarget: info.ID,
				Type:           database.MsgReaction,
			})
		}
	}
	return nil
}

func (portal *Portal) wrapBatchReaction(ctx context.Context, source *User, reaction *waProto.Reaction, mainEventID id.EventID, mainEventTS time.Time) (reactionEvent *event.Event, reactionInfo *types.MessageInfo) {
	var senderJID types.JID
	if reaction.GetKey().GetFromMe() {
		senderJID = source.JID.ToNonAD()
	} else if reaction.GetKey().GetParticipant() != "" {
		senderJID, _ = types.ParseJID(reaction.GetKey().GetParticipant())
	} else if portal.IsPrivateChat() {
		senderJID = portal.Key.JID
	}
	if senderJID.IsEmpty() {
		return
	}
	reactionInfo = &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     portal.Key.JID,
			Sender:   senderJID,
			IsFromMe: reaction.GetKey().GetFromMe(),
			IsGroup:  portal.IsGroupChat(),
		},
		ID:        reaction.GetKey().GetId(),
		Timestamp: mainEventTS,
	}
	puppet := portal.getMessagePuppet(ctx, source, reactionInfo)
	if puppet == nil {
		return
	}
	intent := puppet.IntentFor(portal)
	content := event.ReactionEventContent{
		RelatesTo: event.RelatesTo{
			Type:    event.RelAnnotation,
			EventID: mainEventID,
			Key:     variationselector.Add(reaction.GetText()),
		},
	}
	if rawTS := reaction.GetSenderTimestampMS(); rawTS >= mainEventTS.UnixMilli() && rawTS <= time.Now().UnixMilli() {
		reactionInfo.Timestamp = time.UnixMilli(rawTS)
	}
	wrappedContent := event.Content{Parsed: &content}
	intent.AddDoublePuppetValue(&wrappedContent)
	reactionEvent = &event.Event{
		ID:        portal.deterministicEventID(senderJID, reactionInfo.ID, ""),
		Type:      event.EventReaction,
		Content:   wrappedContent,
		Sender:    intent.UserID,
		Timestamp: reactionInfo.Timestamp.UnixMilli(),
	}
	return
}

func (portal *Portal) wrapBatchEvent(ctx context.Context, info *types.MessageInfo, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, partName string) (*event.Event, error) {
	wrappedContent := event.Content{
		Parsed: content,
		Raw:    extraContent,
	}
	newEventType, err := portal.encrypt(ctx, intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
	}
	intent.AddDoublePuppetValue(&wrappedContent)
	return &event.Event{
		ID:        portal.deterministicEventID(info.Sender, info.ID, partName),
		Sender:    intent.UserID,
		Type:      newEventType,
		Timestamp: info.Timestamp.UnixMilli(),
		Content:   wrappedContent,
	}, nil
}

func (portal *Portal) finishBatch(ctx context.Context, eventIDs []id.EventID, infos []*wrappedInfo) error {
	for i, info := range infos {
		if info == nil {
			continue
		}

		eventID := eventIDs[i]
		portal.markHandled(ctx, nil, info.MessageInfo, eventID, info.SenderMXID, true, false, info.Type, 0, info.Error)
		if info.Type == database.MsgReaction {
			portal.upsertReaction(ctx, nil, info.ReactionTarget, info.Sender, eventID, info.ID)
		}

		if info.ExpiresIn > 0 {
			portal.MarkDisappearing(ctx, eventID, info.ExpiresIn, info.ExpirationStart)
		}
	}
	return nil
}

func (portal *Portal) updateBackfillStatus(ctx context.Context, backfillState *database.BackfillState) {
	backfillStatus := "backfilling"
	if backfillState.BackfillComplete {
		backfillStatus = "complete"
	}

	_, err := portal.bridge.Bot.SendStateEvent(ctx, portal.MXID, BackfillStatusEvent, "", map[string]interface{}{
		"status":          backfillStatus,
		"first_timestamp": backfillState.FirstExpectedTimestamp * 1000,
	})
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send backfill status event to room")
	}
}

// endregion
