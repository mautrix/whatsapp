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
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/variationselector"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

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
			BackfillQuery:   user.bridge.DB.Backfill,
			reCheckChannels: []chan bool{},
			log:             user.log.Sub("BackfillQueue"),
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
	nMostRecent := user.bridge.DB.HistorySync.GetRecentConversations(user.MXID, user.bridge.Config.Bridge.HistorySync.MaxInitialConversations)
	if len(nMostRecent) > 0 {
		user.log.Infofln("%v has passed since the last history sync blob, enqueueing backfills for %d chats", EnqueueBackfillsDelay, len(nMostRecent))
		// Find the portals for all the conversations.
		portals := []*Portal{}
		for _, conv := range nMostRecent {
			jid, err := types.ParseJID(conv.ConversationID)
			if err != nil {
				user.log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.ConversationID, err)
				continue
			}
			portals = append(portals, user.GetPortalByJID(jid))
		}

		user.EnqueueImmediateBackfills(portals)
		user.EnqueueForwardBackfills(portals)
		user.EnqueueDeferredBackfills(portals)

		// Tell the queue to check for new backfill requests.
		user.BackfillQueue.ReCheck()
	}
}

func (user *User) backfillAll() {
	conversations := user.bridge.DB.HistorySync.GetRecentConversations(user.MXID, -1)
	if len(conversations) > 0 {
		user.zlog.Info().
			Int("conversation_count", len(conversations)).
			Msg("Probably received all history sync blobs, now backfilling conversations")
		limit := user.bridge.Config.Bridge.HistorySync.MaxInitialConversations
		// Find the portals for all the conversations.
		for i, conv := range conversations {
			jid, err := types.ParseJID(conv.ConversationID)
			if err != nil {
				user.zlog.Warn().Err(err).
					Str("conversation_id", conv.ConversationID).
					Msg("Failed to parse chat JID in history sync")
				continue
			}
			portal := user.GetPortalByJID(jid)
			if portal.MXID != "" {
				user.zlog.Debug().
					Str("portal_jid", portal.Key.JID.String()).
					Msg("Chat already has a room, deleting messages from database")
				user.bridge.DB.HistorySync.DeleteConversation(user.MXID, portal.Key.JID.String())
			} else if limit < 0 || i < limit {
				err = portal.CreateMatrixRoom(user, nil, true, true)
				if err != nil {
					user.zlog.Err(err).Msg("Failed to create Matrix room for backfill")
				}
			}
		}
	}
}

func (portal *Portal) legacyBackfill(user *User) {
	defer portal.latestEventBackfillLock.Unlock()
	// This should only be called from CreateMatrixRoom which locks latestEventBackfillLock before creating the room.
	if portal.latestEventBackfillLock.TryLock() {
		panic("legacyBackfill() called without locking latestEventBackfillLock")
	}
	// TODO use portal.zlog instead of user.zlog
	log := user.zlog.With().
		Str("portal_jid", portal.Key.JID.String()).
		Str("action", "legacy backfill").
		Logger()
	conv := user.bridge.DB.HistorySync.GetConversation(user.MXID, portal.Key)
	messages := user.bridge.DB.HistorySync.GetMessagesBetween(user.MXID, portal.Key.JID.String(), nil, nil, portal.bridge.Config.Bridge.HistorySync.MessageCount)
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
		portal.handleMessage(user, msgEvt, true)
	}
	if conv != nil {
		isUnread := conv.MarkedAsUnread || conv.UnreadCount > 0
		isTooOld := user.bridge.Config.Bridge.HistorySync.UnreadHoursThreshold > 0 && conv.LastMessageTimestamp.Before(time.Now().Add(time.Duration(-user.bridge.Config.Bridge.HistorySync.UnreadHoursThreshold)*time.Hour))
		shouldMarkAsRead := !isUnread || isTooOld
		if shouldMarkAsRead {
			user.markSelfReadFull(portal)
		}
	}
	log.Debug().Msg("Backfill complete, deleting leftover messages from database")
	user.bridge.DB.HistorySync.DeleteConversation(user.MXID, portal.Key.JID.String())
}

func (user *User) dailyMediaRequestLoop() {
	// Calculate when to do the first set of media retry requests
	now := time.Now()
	userTz, err := time.LoadLocation(user.Timezone)
	if err != nil {
		userTz = now.Local().Location()
	}
	tonightMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, userTz)
	midnightOffset := time.Duration(user.bridge.Config.Bridge.HistorySync.MediaRequests.RequestLocalTime) * time.Minute
	requestStartTime := tonightMidnight.Add(midnightOffset)

	// If the request time for today has already happened, we need to start the
	// request loop tomorrow instead.
	if requestStartTime.Before(now) {
		requestStartTime = requestStartTime.AddDate(0, 0, 1)
	}

	// Wait to start the loop
	user.log.Infof("Waiting until %s to do media retry requests", requestStartTime)
	time.Sleep(time.Until(requestStartTime))

	for {
		mediaBackfillRequests := user.bridge.DB.MediaBackfillRequest.GetMediaBackfillRequestsForUser(user.MXID)
		user.log.Infof("Sending %d media retry requests", len(mediaBackfillRequests))

		// Send all of the media backfill requests for the user at once
		for _, req := range mediaBackfillRequests {
			portal := user.GetPortalByJID(req.PortalKey.JID)
			_, err := portal.requestMediaRetry(user, req.EventID, req.MediaKey)
			if err != nil {
				user.log.Warnf("Failed to send media retry request for %s / %s", req.PortalKey.String(), req.EventID)
				req.Status = database.MediaBackfillRequestStatusRequestFailed
				req.Error = err.Error()
			} else {
				user.log.Debugfln("Sent media retry request for %s / %s", req.PortalKey.String(), req.EventID)
				req.Status = database.MediaBackfillRequestStatusRequested
			}
			req.MediaKey = nil
			req.Upsert()
		}

		// Wait for 24 hours before making requests again
		time.Sleep(24 * time.Hour)
	}
}

func (user *User) backfillInChunks(req *database.Backfill, conv *database.HistorySyncConversation, portal *Portal) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()

	if len(portal.MXID) > 0 && !user.bridge.AS.StateStore.IsInRoom(portal.MXID, user.MXID) {
		portal.ensureUserInvited(user)
	}

	backfillState := user.bridge.DB.Backfill.GetBackfillState(user.MXID, &portal.Key)
	if backfillState == nil {
		backfillState = user.bridge.DB.Backfill.NewBackfillState(user.MXID, &portal.Key)
	}
	backfillState.SetProcessingBatch(true)
	defer backfillState.SetProcessingBatch(false)

	var timeEnd *time.Time
	var forward, shouldMarkAsRead bool
	portal.latestEventBackfillLock.Lock()
	if req.BackfillType == database.BackfillForward {
		// TODO this overrides the TimeStart set when enqueuing the backfill
		//      maybe the enqueue should instead include the prev event ID
		lastMessage := portal.bridge.DB.Message.GetLastInChat(portal.Key)
		start := lastMessage.Timestamp.Add(1 * time.Second)
		req.TimeStart = &start
		// Sending events at the end of the room (= latest events)
		forward = true
	} else {
		firstMessage := portal.bridge.DB.Message.GetFirstInChat(portal.Key)
		if firstMessage != nil {
			end := firstMessage.Timestamp.Add(-1 * time.Second)
			timeEnd = &end
			user.log.Debugfln("Limiting backfill to end at %v", end)
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
	allMsgs := user.bridge.DB.HistorySync.GetMessagesBetween(user.MXID, conv.ConversationID, req.TimeStart, timeEnd, req.MaxTotalEvents)

	sendDisappearedNotice := false
	// If expired messages are on, and a notice has not been sent to this chat
	// about it having disappeared messages at the conversation timestamp, send
	// a notice indicating so.
	if len(allMsgs) == 0 && conv.EphemeralExpiration != nil && *conv.EphemeralExpiration > 0 {
		lastMessage := portal.bridge.DB.Message.GetLastInChat(portal.Key)
		if lastMessage == nil || conv.LastMessageTimestamp.After(lastMessage.Timestamp) {
			sendDisappearedNotice = true
		}
	}

	if !sendDisappearedNotice && len(allMsgs) == 0 {
		user.log.Debugfln("Not backfilling %s: no bridgeable messages found", portal.Key.JID)
		return
	}

	if len(portal.MXID) == 0 {
		user.log.Debugln("Creating portal for", portal.Key.JID, "as part of history sync handling")
		err := portal.CreateMatrixRoom(user, nil, true, false)
		if err != nil {
			user.log.Errorfln("Failed to create room for %s during backfill: %v", portal.Key.JID, err)
			return
		}
	}

	// Update the backfill status here after the room has been created.
	portal.updateBackfillStatus(backfillState)

	if sendDisappearedNotice {
		user.log.Debugfln("Sending notice to %s that there are disappeared messages ending at %v", portal.Key.JID, conv.LastMessageTimestamp)
		resp, err := portal.sendMessage(portal.MainIntent(), event.EventMessage, &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    portal.formatDisappearingMessageNotice(),
		}, nil, conv.LastMessageTimestamp.UnixMilli())

		if err != nil {
			portal.log.Errorln("Error sending disappearing messages notice event")
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
		msg.Insert(nil)
		user.markSelfReadFull(portal)
		return
	}

	user.log.Infofln("Backfilling %d messages in %s, %d messages at a time (queue ID: %d)", len(allMsgs), portal.Key.JID, req.MaxBatchEvents, req.QueueID)
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
			user.log.Debugfln("Backfilling %d messages in %s (queue ID: %d)", len(msgs), portal.Key.JID, req.QueueID)
			portal.backfill(user, msgs, forward, shouldMarkAsRead)
		}
	}
	user.log.Debugfln("Finished backfilling %d messages in %s (queue ID: %d)", len(allMsgs), portal.Key.JID, req.QueueID)
	err := user.bridge.DB.HistorySync.DeleteMessages(user.MXID, conv.ConversationID, allMsgs)
	if err != nil {
		user.log.Warnfln("Failed to delete %d history sync messages after backfilling (queue ID: %d): %v", len(allMsgs), req.QueueID, err)
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
		backfillState.Upsert()
		portal.updateBackfillStatus(backfillState)
	}
}

func (user *User) storeHistorySync(evt *waProto.HistorySync) {
	if evt == nil || evt.SyncType == nil {
		return
	}
	log := user.bridge.ZLog.With().
		Str("method", "User.storeHistorySync").
		Str("user_id", user.MXID.String()).
		Str("sync_type", evt.GetSyncType().String()).
		Uint32("chunk_order", evt.GetChunkOrder()).
		Uint32("progress", evt.GetProgress()).
		Logger()
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
		portal := user.GetPortalByJID(jid)
		log := log.With().
			Str("chat_jid", portal.Key.JID.String()).
			Int("msg_count", len(conv.GetMessages())).
			Logger()

		historySyncConversation := user.bridge.DB.HistorySync.NewConversationWithValues(
			user.MXID,
			conv.GetId(),
			&portal.Key,
			getConversationTimestamp(conv),
			conv.GetMuteEndTime(),
			conv.GetArchived(),
			conv.GetPinned(),
			conv.GetDisappearingMode().GetInitiator(),
			conv.GetEndOfHistoryTransferType(),
			conv.EphemeralExpiration,
			conv.GetMarkedAsUnread(),
			conv.GetUnreadCount())
		historySyncConversation.Upsert()
		var minTime, maxTime time.Time
		var minTimeIndex, maxTimeIndex int

		successfullySaved := 0
		unsupportedTypes := 0
		for i, rawMsg := range conv.GetMessages() {
			// Don't store messages that will just be skipped.
			msgEvt, err := user.Client.ParseWebMessage(portal.Key.JID, rawMsg.GetMessage())
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
			if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
				unsupportedTypes++
				continue
			}

			// Don't store unsupported messages.
			if !containsSupportedMessage(msgEvt.Message) {
				unsupportedTypes++
				continue
			}

			message, err := user.bridge.DB.HistorySync.NewMessageWithValues(user.MXID, conv.GetId(), msgEvt.Info.ID, rawMsg)
			if err != nil {
				log.Error().Err(err).
					Int("msg_index", i).
					Str("msg_id", msgEvt.Info.ID).
					Time("msg_time", msgEvt.Info.Timestamp).
					Msg("Failed to save historical message")
				continue
			}
			err = message.Insert()
			if err != nil {
				log.Error().Err(err).
					Int("msg_index", i).
					Str("msg_id", msgEvt.Info.ID).
					Time("msg_time", msgEvt.Info.Timestamp).
					Msg("Failed to save historical message")
			}
			successfullySaved++
		}
		successfullySavedTotal += successfullySaved
		log.Debug().
			Int("saved_count", successfullySaved).
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

func (user *User) EnqueueImmediateBackfills(portals []*Portal) {
	for priority, portal := range portals {
		maxMessages := user.bridge.Config.Bridge.HistorySync.Immediate.MaxEvents
		initialBackfill := user.bridge.DB.Backfill.NewWithValues(user.MXID, database.BackfillImmediate, priority, &portal.Key, nil, maxMessages, maxMessages, 0)
		initialBackfill.Insert()
	}
}

func (user *User) EnqueueDeferredBackfills(portals []*Portal) {
	numPortals := len(portals)
	for stageIdx, backfillStage := range user.bridge.Config.Bridge.HistorySync.Deferred {
		for portalIdx, portal := range portals {
			var startDate *time.Time = nil
			if backfillStage.StartDaysAgo > 0 {
				startDaysAgo := time.Now().AddDate(0, 0, -backfillStage.StartDaysAgo)
				startDate = &startDaysAgo
			}
			backfillMessages := user.bridge.DB.Backfill.NewWithValues(
				user.MXID, database.BackfillDeferred, stageIdx*numPortals+portalIdx, &portal.Key, startDate, backfillStage.MaxBatchEvents, -1, backfillStage.BatchDelay)
			backfillMessages.Insert()
		}
	}
}

func (user *User) EnqueueForwardBackfills(portals []*Portal) {
	for priority, portal := range portals {
		lastMsg := user.bridge.DB.Message.GetLastInChat(portal.Key)
		if lastMsg == nil {
			continue
		}
		backfill := user.bridge.DB.Backfill.NewWithValues(
			user.MXID, database.BackfillForward, priority, &portal.Key, &lastMsg.Timestamp, -1, -1, 0)
		backfill.Insert()
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
	PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}

	BackfillStatusEvent = event.Type{Type: "com.beeper.backfill_status", Class: event.StateEventType}
)

func (portal *Portal) backfill(source *User, messages []*waProto.WebMessageInfo, isForward, atomicMarkAsRead bool) *mautrix.RespBeeperBatchSend {
	var req mautrix.ReqBeeperBatchSend
	var infos []*wrappedInfo

	req.Forward = isForward
	if atomicMarkAsRead {
		req.MarkReadBy = source.MXID
	}

	portal.log.Infofln("Processing history sync with %d messages (forward: %t)", len(messages), isForward)
	// The messages are ordered newest to oldest, so iterate them in reverse order.
	for i := len(messages) - 1; i >= 0; i-- {
		webMsg := messages[i]
		msgEvt, err := source.Client.ParseWebMessage(portal.Key.JID, webMsg)
		if err != nil {
			continue
		}

		msgType := getMessageType(msgEvt.Message)
		if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
			if msgType != "ignore" {
				portal.log.Debugfln("Skipping message %s with unknown type in backfill", msgEvt.Info.ID)
			}
			continue
		}
		if webMsg.GetPushName() != "" && webMsg.GetPushName() != "-" {
			existingContact, _ := source.Client.Store.Contacts.GetContact(msgEvt.Info.Sender)
			if !existingContact.Found || existingContact.PushName == "" {
				changed, _, err := source.Client.Store.Contacts.PutPushName(msgEvt.Info.Sender, webMsg.GetPushName())
				if err != nil {
					source.log.Errorfln("Failed to save push name of %s from historical message in device store: %v", msgEvt.Info.Sender, err)
				} else if changed {
					source.log.Debugfln("Got push name %s for %s from historical message", webMsg.GetPushName(), msgEvt.Info.Sender)
				}
			}
		}
		puppet := portal.getMessagePuppet(source, &msgEvt.Info)
		if puppet == nil {
			continue
		}

		converted := portal.convertMessage(puppet.IntentFor(portal), source, &msgEvt.Info, msgEvt.Message, true)
		if converted == nil {
			portal.log.Debugfln("Skipping unsupported message %s in backfill", msgEvt.Info.ID)
			continue
		}
		if converted.ReplyTo != nil {
			portal.SetReply(msgEvt.Info.ID, converted.Content, converted.ReplyTo, true)
		}
		err = portal.appendBatchEvents(source, converted, &msgEvt.Info, webMsg, &req.Events, &infos)
		if err != nil {
			portal.log.Errorfln("Error handling message %s during backfill: %v", msgEvt.Info.ID, err)
		}
	}
	portal.log.Infofln("Made %d Matrix events from messages in batch", len(req.Events))

	if len(req.Events) == 0 {
		return nil
	}

	resp, err := portal.MainIntent().BeeperBatchSend(portal.MXID, &req)
	if err != nil {
		portal.log.Errorln("Error batch sending messages:", err)
		return nil
	} else {
		txn, err := portal.bridge.DB.Begin()
		if err != nil {
			portal.log.Errorln("Failed to start transaction to save batch messages:", err)
			return nil
		}

		portal.finishBatch(txn, resp.EventIDs, infos)

		err = txn.Commit()
		if err != nil {
			portal.log.Errorln("Failed to commit transaction to save batch messages:", err)
			return nil
		}
		if portal.bridge.Config.Bridge.HistorySync.MediaRequests.AutoRequestMedia {
			go portal.requestMediaRetries(source, resp.EventIDs, infos)
		}
		return resp
	}
}

func (portal *Portal) requestMediaRetries(source *User, eventIDs []id.EventID, infos []*wrappedInfo) {
	for i, info := range infos {
		if info != nil && info.Error == database.MsgErrMediaNotFound && info.MediaKey != nil {
			switch portal.bridge.Config.Bridge.HistorySync.MediaRequests.RequestMethod {
			case config.MediaRequestMethodImmediate:
				err := source.Client.SendMediaRetryReceipt(info.MessageInfo, info.MediaKey)
				if err != nil {
					portal.log.Warnfln("Failed to send post-backfill media retry request for %s: %v", info.ID, err)
				} else {
					portal.log.Debugfln("Sent post-backfill media retry request for %s", info.ID)
				}
			case config.MediaRequestMethodLocalTime:
				req := portal.bridge.DB.MediaBackfillRequest.NewMediaBackfillRequestWithValues(source.MXID, &portal.Key, eventIDs[i], info.MediaKey)
				req.Upsert()
			}
		}
	}
}

func (portal *Portal) appendBatchEvents(source *User, converted *ConvertedMessage, info *types.MessageInfo, raw *waProto.WebMessageInfo, eventsArray *[]*event.Event, infoArray *[]*wrappedInfo) error {
	if portal.bridge.Config.Bridge.CaptionInMessage {
		converted.MergeCaption()
	}
	mainEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Content, converted.Extra, "")
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
		captionEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Caption, nil, "caption")
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
			subEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, subEvtContent, nil, fmt.Sprintf("multi-%d", i))
			if err != nil {
				return err
			}
			*eventsArray = append(*eventsArray, subEvt)
			*infoArray = append(*infoArray, nil)
		}
	}
	for _, reaction := range raw.GetReactions() {
		reactionEvent, reactionInfo := portal.wrapBatchReaction(source, reaction, mainEvt.ID, info.Timestamp)
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

func (portal *Portal) wrapBatchReaction(source *User, reaction *waProto.Reaction, mainEventID id.EventID, mainEventTS time.Time) (reactionEvent *event.Event, reactionInfo *types.MessageInfo) {
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
	puppet := portal.getMessagePuppet(source, reactionInfo)
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
	if rawTS := reaction.GetSenderTimestampMs(); rawTS >= mainEventTS.UnixMilli() && rawTS <= time.Now().UnixMilli() {
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

func (portal *Portal) wrapBatchEvent(info *types.MessageInfo, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}, partName string) (*event.Event, error) {
	wrappedContent := event.Content{
		Parsed: content,
		Raw:    extraContent,
	}
	newEventType, err := portal.encrypt(intent, &wrappedContent, eventType)
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

func (portal *Portal) finishBatch(txn dbutil.Transaction, eventIDs []id.EventID, infos []*wrappedInfo) {
	for i, info := range infos {
		if info == nil {
			continue
		}

		eventID := eventIDs[i]
		portal.markHandled(txn, nil, info.MessageInfo, eventID, info.SenderMXID, true, false, info.Type, info.Error)
		if info.Type == database.MsgReaction {
			portal.upsertReaction(txn, nil, info.ReactionTarget, info.Sender, eventID, info.ID)
		}

		if info.ExpiresIn > 0 {
			portal.MarkDisappearing(txn, eventID, info.ExpiresIn, info.ExpirationStart)
		}
	}
	portal.log.Infofln("Successfully sent %d events", len(eventIDs))
}

func (portal *Portal) updateBackfillStatus(backfillState *database.BackfillState) {
	backfillStatus := "backfilling"
	if backfillState.BackfillComplete {
		backfillStatus = "complete"
	}

	_, err := portal.bridge.Bot.SendStateEvent(portal.MXID, BackfillStatusEvent, "", map[string]interface{}{
		"status":          backfillStatus,
		"first_timestamp": backfillState.FirstExpectedTimestamp * 1000,
	})
	if err != nil {
		portal.log.Errorln("Error sending backfill status event:", err)
	}
}

// endregion
