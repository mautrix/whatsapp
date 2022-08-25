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

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridge/bridgeconfig"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"maunium.net/go/mautrix-whatsapp/config"
	"maunium.net/go/mautrix-whatsapp/database"
)

// region User history sync handling

type wrappedInfo struct {
	*types.MessageInfo
	Type  database.MessageType
	Error database.MessageErrorType

	MediaKey []byte

	ExpirationStart uint64
	ExpiresIn       uint32
}

func (user *User) handleHistorySyncsLoop() {
	if !user.bridge.Config.Bridge.HistorySync.Backfill {
		return
	}

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

	if user.bridge.Config.Bridge.HistorySync.MediaRequests.AutoRequestMedia &&
		user.bridge.Config.Bridge.HistorySync.MediaRequests.RequestMethod == config.MediaRequestMethodLocalTime {
		go user.dailyMediaRequestLoop()
	}

	// Always save the history syncs for the user. If they want to enable
	// backfilling in the future, we will have it in the database.
	for evt := range user.historySyncs {
		user.handleHistorySync(user.BackfillQueue, evt.Data)
	}
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

	if !user.shouldCreatePortalForHistorySync(conv, portal) {
		return
	}

	backfillState := user.bridge.DB.Backfill.GetBackfillState(user.MXID, &portal.Key)
	if backfillState == nil {
		backfillState = user.bridge.DB.Backfill.NewBackfillState(user.MXID, &portal.Key)
	}
	backfillState.SetProcessingBatch(true)
	defer backfillState.SetProcessingBatch(false)

	var forwardPrevID id.EventID
	var timeEnd *time.Time
	var isLatestEvents bool
	portal.latestEventBackfillLock.Lock()
	if req.BackfillType == database.BackfillForward {
		// TODO this overrides the TimeStart set when enqueuing the backfill
		//      maybe the enqueue should instead include the prev event ID
		lastMessage := portal.bridge.DB.Message.GetLastInChat(portal.Key)
		forwardPrevID = lastMessage.MXID
		start := lastMessage.Timestamp.Add(1 * time.Second)
		req.TimeStart = &start
		// Sending events at the end of the room (= latest events)
		isLatestEvents = true
	} else {
		firstMessage := portal.bridge.DB.Message.GetFirstInChat(portal.Key)
		if firstMessage != nil {
			end := firstMessage.Timestamp.Add(-1 * time.Second)
			timeEnd = &end
			user.log.Debugfln("Limiting backfill to end at %v", end)
		} else {
			// Portal is empty -> events are latest
			isLatestEvents = true
		}
	}
	if !isLatestEvents {
		// We'll use normal batch sending, so no need to keep blocking new message processing
		portal.latestEventBackfillLock.Unlock()
	} else {
		// This might involve sending events at the end of the room as non-historical events,
		// make sure we don't process messages until this is done.
		defer portal.latestEventBackfillLock.Unlock()
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
		msg.Sent = true
		msg.Type = database.MsgFake
		msg.Insert(nil)
		return
	}

	user.log.Infofln("Backfilling %d messages in %s, %d messages at a time (queue ID: %d)", len(allMsgs), portal.Key.JID, req.MaxBatchEvents, req.QueueID)
	toBackfill := allMsgs[0:]
	var insertionEventIds []id.EventID
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
			resp := portal.backfill(user, msgs, req.BackfillType == database.BackfillForward, isLatestEvents, forwardPrevID)
			if resp != nil && (resp.BaseInsertionEventID != "" || !isLatestEvents) {
				insertionEventIds = append(insertionEventIds, resp.BaseInsertionEventID)
			}
		}
	}
	user.log.Debugfln("Finished backfilling %d messages in %s (queue ID: %d)", len(allMsgs), portal.Key.JID, req.QueueID)
	if len(insertionEventIds) > 0 {
		portal.sendPostBackfillDummy(
			time.Unix(int64(allMsgs[0].GetMessageTimestamp()), 0),
			insertionEventIds[0])
	}
	user.log.Debugfln("Deleting %d history sync messages after backfilling (queue ID: %d)", len(allMsgs), req.QueueID)
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

	if !conv.MarkedAsUnread && conv.UnreadCount == 0 {
		user.markSelfReadFull(portal)
	} else if user.bridge.Config.Bridge.SyncManualMarkedUnread {
		user.markUnread(portal, true)
	}
}

func (user *User) shouldCreatePortalForHistorySync(conv *database.HistorySyncConversation, portal *Portal) bool {
	if len(portal.MXID) > 0 {
		if !user.bridge.AS.StateStore.IsInRoom(portal.MXID, user.MXID) {
			portal.ensureUserInvited(user)
		}
		// Portal exists, let backfill continue
		return true
	} else if !user.bridge.Config.Bridge.HistorySync.CreatePortals {
		user.log.Debugfln("Not creating portal for %s: creating rooms from history sync is disabled", portal.Key.JID)
		return false
	} else {
		// Portal doesn't exist, but should be created
		return true
	}
}

func (user *User) handleHistorySync(backfillQueue *BackfillQueue, evt *waProto.HistorySync) {
	if evt == nil || evt.SyncType == nil || evt.GetSyncType() == waProto.HistorySync_INITIAL_STATUS_V3 || evt.GetSyncType() == waProto.HistorySync_PUSH_NAME {
		return
	}
	description := fmt.Sprintf("type %s, %d conversations, chunk order %d, progress: %d", evt.GetSyncType(), len(evt.GetConversations()), evt.GetChunkOrder(), evt.GetProgress())
	user.log.Infoln("Storing history sync with", description)

	for _, conv := range evt.GetConversations() {
		jid, err := types.ParseJID(conv.GetId())
		if err != nil {
			user.log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.GetId(), err)
			continue
		} else if jid.Server == types.BroadcastServer {
			user.log.Debugfln("Skipping broadcast list %s in history sync", jid)
			continue
		}
		portal := user.GetPortalByJID(jid)

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

		for _, rawMsg := range conv.GetMessages() {
			// Don't store messages that will just be skipped.
			msgEvt, err := user.Client.ParseWebMessage(portal.Key.JID, rawMsg.GetMessage())
			if err != nil {
				user.log.Warnln("Dropping historical message due to info parse error:", err)
				continue
			}

			msgType := getMessageType(msgEvt.Message)
			if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
				continue
			}

			// Don't store unsupported messages.
			if !containsSupportedMessage(msgEvt.Message) {
				continue
			}

			message, err := user.bridge.DB.HistorySync.NewMessageWithValues(user.MXID, conv.GetId(), msgEvt.Info.ID, rawMsg)
			if err != nil {
				user.log.Warnfln("Failed to save message %s in %s. Error: %+v", msgEvt.Info.ID, conv.GetId(), err)
				continue
			}
			message.Insert()
		}
	}

	// If this was the initial bootstrap, enqueue immediate backfills for the
	// most recent portals. If it's the last history sync event, start
	// backfilling the rest of the history of the portals.
	if user.bridge.Config.Bridge.HistorySync.Backfill {
		if evt.GetSyncType() != waProto.HistorySync_INITIAL_BOOTSTRAP && evt.GetProgress() < 98 {
			return
		}

		nMostRecent := user.bridge.DB.HistorySync.GetNMostRecentConversations(user.MXID, user.bridge.Config.Bridge.HistorySync.MaxInitialConversations)
		if len(nMostRecent) > 0 {
			// Find the portals for all of the conversations.
			portals := []*Portal{}
			for _, conv := range nMostRecent {
				jid, err := types.ParseJID(conv.ConversationID)
				if err != nil {
					user.log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.ConversationID, err)
					continue
				}
				portals = append(portals, user.GetPortalByJID(jid))
			}

			switch evt.GetSyncType() {
			case waProto.HistorySync_INITIAL_BOOTSTRAP:
				// Enqueue immediate backfills for the most recent messages first.
				user.EnqueueImmedateBackfills(portals)
			case waProto.HistorySync_FULL, waProto.HistorySync_RECENT:
				user.EnqueueForwardBackfills(portals)
				// Enqueue deferred backfills as configured.
				user.EnqueueDeferredBackfills(portals)
			}

			// Tell the queue to check for new backfill requests.
			backfillQueue.ReCheck()
		}
	}
}

func getConversationTimestamp(conv *waProto.Conversation) uint64 {
	convTs := conv.GetConversationTimestamp()
	if convTs == 0 && len(conv.GetMessages()) > 0 {
		convTs = conv.Messages[0].GetMessage().GetMessageTimestamp()
	}
	return convTs
}

func (user *User) EnqueueImmedateBackfills(portals []*Portal) {
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

func (portal *Portal) deterministicEventID(sender types.JID, messageID types.MessageID) id.EventID {
	data := fmt.Sprintf("%s/whatsapp/%s/%s", portal.MXID, sender.User, messageID)
	sum := sha256.Sum256([]byte(data))
	return id.EventID(fmt.Sprintf("$%s:whatsapp.com", base64.RawURLEncoding.EncodeToString(sum[:])))
}

var (
	PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
	PreBackfillDummyEvent    = event.Type{Type: "fi.mau.dummy.pre_backfill", Class: event.MessageEventType}

	HistorySyncMarker = event.Type{Type: "org.matrix.msc2716.marker", Class: event.MessageEventType}

	BackfillStatusEvent = event.Type{Type: "com.beeper.backfill_status", Class: event.StateEventType}
)

func (portal *Portal) backfill(source *User, messages []*waProto.WebMessageInfo, isForward, isLatest bool, prevEventID id.EventID) *mautrix.RespBatchSend {
	var req mautrix.ReqBatchSend
	var infos []*wrappedInfo

	if !isForward {
		if portal.FirstEventID != "" || portal.NextBatchID != "" {
			req.PrevEventID = portal.FirstEventID
			req.BatchID = portal.NextBatchID
		} else {
			portal.log.Warnfln("Can't backfill %d messages through %s to chat: first event ID not known", len(messages), source.MXID)
			return nil
		}
	} else {
		req.PrevEventID = prevEventID
	}
	req.BeeperNewMessages = isLatest && req.BatchID == ""

	beforeFirstMessageTimestampMillis := (int64(messages[len(messages)-1].GetMessageTimestamp()) * 1000) - 1
	req.StateEventsAtStart = make([]*event.Event, 0)

	addedMembers := make(map[id.UserID]struct{})
	addMember := func(puppet *Puppet) {
		if _, alreadyAdded := addedMembers[puppet.MXID]; alreadyAdded {
			return
		}
		mxid := puppet.MXID.String()
		content := event.MemberEventContent{
			Membership:  event.MembershipJoin,
			Displayname: puppet.Displayname,
			AvatarURL:   puppet.AvatarURL.CUString(),
		}
		inviteContent := content
		inviteContent.Membership = event.MembershipInvite
		req.StateEventsAtStart = append(req.StateEventsAtStart, &event.Event{
			Type:      event.StateMember,
			Sender:    portal.MainIntent().UserID,
			StateKey:  &mxid,
			Timestamp: beforeFirstMessageTimestampMillis,
			Content:   event.Content{Parsed: &inviteContent},
		}, &event.Event{
			Type:      event.StateMember,
			Sender:    puppet.MXID,
			StateKey:  &mxid,
			Timestamp: beforeFirstMessageTimestampMillis,
			Content:   event.Content{Parsed: &content},
		})
		addedMembers[puppet.MXID] = struct{}{}
	}

	portal.log.Infofln("Processing history sync with %d messages (forward: %t, latest: %t, prev: %s, batch: %s)", len(messages), isForward, isLatest, req.PrevEventID, req.BatchID)
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
		intent := puppet.IntentFor(portal)
		if intent.IsCustomPuppet && !portal.bridge.Config.CanDoublePuppetBackfill(puppet.CustomMXID) {
			intent = puppet.DefaultIntent()
		}

		converted := portal.convertMessage(intent, source, &msgEvt.Info, msgEvt.Message, true)
		if converted == nil {
			portal.log.Debugfln("Skipping unsupported message %s in backfill", msgEvt.Info.ID)
			continue
		}
		if !intent.IsCustomPuppet && !portal.bridge.StateStore.IsInRoom(portal.MXID, puppet.MXID) {
			addMember(puppet)
		}
		if converted.ReplyTo != nil {
			portal.SetReply(converted.Content, converted.ReplyTo, true)
		}
		err = portal.appendBatchEvents(converted, &msgEvt.Info, webMsg.GetEphemeralStartTimestamp(), &req.Events, &infos)
		if err != nil {
			portal.log.Errorfln("Error handling message %s during backfill: %v", msgEvt.Info.ID, err)
		}
	}
	portal.log.Infofln("Made %d Matrix events from messages in batch", len(req.Events))

	if len(req.Events) == 0 {
		return nil
	}

	if len(req.BatchID) == 0 || isForward {
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, PreBackfillDummyEvent, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending pre-backfill dummy event:", err)
		}
	}

	resp, err := portal.MainIntent().BatchSend(portal.MXID, &req)
	if err != nil {
		portal.log.Errorln("Error batch sending messages:", err)
		return nil
	} else {
		txn, err := portal.bridge.DB.Begin()
		if err != nil {
			portal.log.Errorln("Failed to start transaction to save batch messages:", err)
			return nil
		}

		// Do the following block in the transaction
		{
			portal.finishBatch(txn, resp.EventIDs, infos)
			portal.NextBatchID = resp.NextBatchID
			portal.Update(txn)
		}

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

func (portal *Portal) appendBatchEvents(converted *ConvertedMessage, info *types.MessageInfo, expirationStart uint64, eventsArray *[]*event.Event, infoArray *[]*wrappedInfo) error {
	mainEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Content, converted.Extra)
	if err != nil {
		return err
	}
	if portal.bridge.Config.Bridge.CaptionInMessage {
		converted.MergeCaption()
	}
	if converted.Caption != nil {
		captionEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Caption, nil)
		if err != nil {
			return err
		}
		*eventsArray = append(*eventsArray, mainEvt, captionEvt)
		*infoArray = append(*infoArray, &wrappedInfo{info, database.MsgNormal, converted.Error, converted.MediaKey, expirationStart, converted.ExpiresIn}, nil)
	} else {
		*eventsArray = append(*eventsArray, mainEvt)
		*infoArray = append(*infoArray, &wrappedInfo{info, database.MsgNormal, converted.Error, converted.MediaKey, expirationStart, converted.ExpiresIn})
	}
	if converted.MultiEvent != nil {
		for _, subEvtContent := range converted.MultiEvent {
			subEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, subEvtContent, nil)
			if err != nil {
				return err
			}
			*eventsArray = append(*eventsArray, subEvt)
			*infoArray = append(*infoArray, nil)
		}
	}
	return nil
}

func (portal *Portal) wrapBatchEvent(info *types.MessageInfo, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}) (*event.Event, error) {
	wrappedContent := event.Content{
		Parsed: content,
		Raw:    extraContent,
	}
	newEventType, err := portal.encrypt(intent, &wrappedContent, eventType)
	if err != nil {
		return nil, err
	}
	if newEventType != eventType {
		intent.AddDoublePuppetValue(&wrappedContent)
	}
	var eventID id.EventID
	if portal.bridge.Config.Homeserver.Software == bridgeconfig.SoftwareHungry {
		eventID = portal.deterministicEventID(info.Sender, info.ID)
	}

	return &event.Event{
		ID:        eventID,
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
		portal.markHandled(txn, nil, info.MessageInfo, eventID, true, false, info.Type, info.Error)

		if info.ExpiresIn > 0 {
			if info.ExpirationStart > 0 {
				remainingSeconds := time.Unix(int64(info.ExpirationStart), 0).Add(time.Duration(info.ExpiresIn) * time.Second).Sub(time.Now()).Seconds()
				portal.log.Debugfln("Disappearing history sync message: expires in %d, started at %d, remaining %d", info.ExpiresIn, info.ExpirationStart, int(remainingSeconds))
				portal.MarkDisappearing(eventID, uint32(remainingSeconds), true)
			} else {
				portal.log.Debugfln("Disappearing history sync message: expires in %d (not started)", info.ExpiresIn)
				portal.MarkDisappearing(eventID, info.ExpiresIn, false)
			}
		}
	}
	portal.log.Infofln("Successfully sent %d events", len(eventIDs))
}

func (portal *Portal) sendPostBackfillDummy(lastTimestamp time.Time, insertionEventId id.EventID) {
	resp, err := portal.MainIntent().SendMessageEvent(portal.MXID, HistorySyncMarker, map[string]interface{}{
		"org.matrix.msc2716.marker.insertion": insertionEventId,
		//"m.marker.insertion":                  insertionEventId,
	})
	if err != nil {
		portal.log.Errorln("Error sending post-backfill dummy event:", err)
		return
	}
	msg := portal.bridge.DB.Message.New()
	msg.Chat = portal.Key
	msg.MXID = resp.EventID
	msg.JID = types.MessageID(resp.EventID)
	msg.Timestamp = lastTimestamp.Add(1 * time.Second)
	msg.Sent = true
	msg.Type = database.MsgFake
	msg.Insert(nil)
}

func (portal *Portal) updateBackfillStatus(backfillState *database.BackfillState) {
	backfillStatus := "backfilling"
	if backfillState.BackfillComplete {
		backfillStatus = "complete"
	}

	_, err := portal.MainIntent().SendStateEvent(portal.MXID, BackfillStatusEvent, "", map[string]interface{}{
		"status":          backfillStatus,
		"first_timestamp": backfillState.FirstExpectedTimestamp * 1000,
	})
	if err != nil {
		portal.log.Errorln("Error sending backfill status event:", err)
	}
}

// endregion
