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
	"fmt"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

// region User history sync handling

type wrappedInfo struct {
	*types.MessageInfo
	Type  database.MessageType
	Error database.MessageErrorType

	ExpirationStart uint64
	ExpiresIn       uint32
}

func (user *User) handleHistorySyncsLoop() {
	if !user.bridge.Config.Bridge.HistorySync.Backfill {
		return
	}

	reCheckQueue := make(chan bool, 1)
	// Start the backfill queue.
	user.BackfillQueue = &BackfillQueue{
		BackfillQuery:             user.bridge.DB.BackfillQuery,
		ImmediateBackfillRequests: make(chan *database.Backfill, 1),
		DeferredBackfillRequests:  make(chan *database.Backfill, 1),
		ReCheckQueue:              make(chan bool, 1),
		log:                       user.log.Sub("BackfillQueue"),
	}
	reCheckQueue = user.BackfillQueue.ReCheckQueue

	// Immediate backfills can be done in parallel
	for i := 0; i < user.bridge.Config.Bridge.HistorySync.Immediate.WorkerCount; i++ {
		go user.handleBackfillRequestsLoop(user.BackfillQueue.ImmediateBackfillRequests)
	}

	// Deferred backfills should be handled synchronously so as not to
	// overload the homeserver. Users can configure their backfill stages
	// to be more or less aggressive with backfilling at this stage.
	go user.handleBackfillRequestsLoop(user.BackfillQueue.DeferredBackfillRequests)
	go user.BackfillQueue.RunLoops(user)

	// Always save the history syncs for the user. If they want to enable
	// backfilling in the future, we will have it in the database.
	for evt := range user.historySyncs {
		user.handleHistorySync(reCheckQueue, evt.Data)
	}
}

func (user *User) handleBackfillRequestsLoop(backfillRequests chan *database.Backfill) {
	for req := range backfillRequests {
		user.log.Debugfln("Handling backfill request %s", req)
		conv := user.bridge.DB.HistorySyncQuery.GetConversation(user.MXID, req.Portal)
		if conv == nil {
			user.log.Debugfln("Could not find history sync conversation data for %s", req.Portal.String())
			continue
		}
		portal := user.GetPortalByJID(conv.PortalKey.JID)

		if req.BackfillType == database.BackfillMedia {
			startTime := time.Unix(0, 0)
			if req.TimeStart != nil {
				startTime = *req.TimeStart
			}
			endTime := time.Now()
			if req.TimeEnd != nil {
				endTime = *req.TimeEnd
			}

			user.log.Debugfln("Backfilling media from %v to %v for %s", startTime, endTime, portal.Key.String())

			// Go through all of the messages in the given time range,
			// requesting any media that errored.
			requested := 0
			for _, msg := range user.bridge.DB.Message.GetMessagesBetween(portal.Key, startTime, endTime) {
				if requested > 0 && requested%req.MaxBatchEvents == 0 {
					time.Sleep(time.Duration(req.BatchDelay) * time.Second)
				}
				if msg.Error == database.MsgErrMediaNotFound {
					portal.requestMediaRetry(user, msg.MXID)
					requested += 1
				}
			}
		} else {
			// Update the client store with basic chat settings.
			if conv.MuteEndTime.After(time.Now()) {
				user.Client.Store.ChatSettings.PutMutedUntil(conv.PortalKey.JID, conv.MuteEndTime)
			}
			if conv.Archived {
				user.Client.Store.ChatSettings.PutArchived(conv.PortalKey.JID, true)
			}
			if conv.Pinned > 0 {
				user.Client.Store.ChatSettings.PutPinned(conv.PortalKey.JID, true)
			}

			if conv.EphemeralExpiration != nil && portal.ExpirationTime != *conv.EphemeralExpiration {
				portal.ExpirationTime = *conv.EphemeralExpiration
				portal.Update()
			}

			user.createOrUpdatePortalAndBackfillWithLock(req, conv, portal)
		}
	}
}

func (user *User) createOrUpdatePortalAndBackfillWithLock(req *database.Backfill, conv *database.HistorySyncConversation, portal *Portal) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()

	if !user.shouldCreatePortalForHistorySync(conv, portal) {
		return
	}

	allMsgs := user.bridge.DB.HistorySyncQuery.GetMessagesBetween(user.MXID, conv.ConversationID, req.TimeStart, req.TimeEnd, req.MaxTotalEvents)
	if len(allMsgs) == 0 {
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

	user.log.Debugfln("Backfilling %d messages in %s, %d messages at a time", len(allMsgs), portal.Key.JID, req.MaxBatchEvents)
	toBackfill := allMsgs[0:]
	var insertionEventIds []id.EventID
	for len(toBackfill) > 0 {
		var msgs []*waProto.WebMessageInfo
		if len(toBackfill) <= req.MaxBatchEvents {
			msgs = toBackfill
			toBackfill = nil
		} else {
			msgs = toBackfill[:req.MaxBatchEvents]
			toBackfill = toBackfill[req.MaxBatchEvents:]
		}

		if len(msgs) > 0 {
			time.Sleep(time.Duration(req.BatchDelay) * time.Second)
			user.log.Debugfln("Backfilling %d messages in %s (queue ID: %d)", len(msgs), portal.Key.JID, req.QueueID)
			insertionEventIds = append(insertionEventIds, portal.backfill(user, msgs)...)
		}
	}
	user.log.Debugfln("Finished backfilling %d messages in %s (queue ID: %d)", len(allMsgs), portal.Key.JID, req.QueueID)
	if len(insertionEventIds) > 0 {
		portal.sendPostBackfillDummy(
			time.Unix(int64(allMsgs[0].GetMessageTimestamp()), 0),
			insertionEventIds[0])
	}
	user.log.Debugfln("Deleting %d history sync messages after backfilling", len(allMsgs))
	err := user.bridge.DB.HistorySyncQuery.DeleteMessages(user.MXID, conv.ConversationID, allMsgs)
	if err != nil {
		user.log.Warnfln("Failed to delete %d history sync messages after backfilling: %v", len(allMsgs), err)
	}

	if !conv.MarkedAsUnread && conv.UnreadCount == 0 {
		user.markSelfReadFull(portal)
	}
}

func (user *User) shouldCreatePortalForHistorySync(conv *database.HistorySyncConversation, portal *Portal) bool {
	if len(portal.MXID) > 0 {
		user.log.Debugfln("Portal for %s already exists, ensuring user is invited", portal.Key.JID)
		portal.ensureUserInvited(user)
		// Portal exists, let backfill continue
		return true
	} else if !user.bridge.Config.Bridge.HistorySync.CreatePortals {
		user.log.Debugfln("Not creating portal for %s: creating rooms from history sync is disabled", portal.Key.JID)
	} else {
		// Portal doesn't exist, but should be created
		return true
	}
	// Portal shouldn't be created, reason logged above
	return false
}

func (user *User) handleHistorySync(reCheckQueue chan bool, evt *waProto.HistorySync) {
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

		historySyncConversation := user.bridge.DB.HistorySyncQuery.NewConversationWithValues(
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

		for _, msg := range conv.GetMessages() {
			// Don't store messages that will just be skipped.
			wmi := msg.GetMessage()
			msgType := getMessageType(wmi.GetMessage())
			if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
				continue
			}

			// Don't store unsupported messages.
			if !containsSupportedMessage(msg.GetMessage().GetMessage()) {
				continue
			}

			message, err := user.bridge.DB.HistorySyncQuery.NewMessageWithValues(user.MXID, conv.GetId(), msg.Message.GetKey().GetId(), msg)
			if err != nil {
				user.log.Warnfln("Failed to save message %s in %s. Error: %+v", msg.Message.Key.Id, conv.GetId(), err)
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

		nMostRecent := user.bridge.DB.HistorySyncQuery.GetNMostRecentConversations(user.MXID, user.bridge.Config.Bridge.HistorySync.MaxInitialConversations)
		var priorityCounter int
		for _, conv := range nMostRecent {
			jid, err := types.ParseJID(conv.ConversationID)
			if err != nil {
				user.log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.ConversationID, err)
				continue
			}
			portal := user.GetPortalByJID(jid)

			switch evt.GetSyncType() {
			case waProto.HistorySync_INITIAL_BOOTSTRAP:
				// Enqueue immediate backfills for the most recent messages first.
				user.EnqueueImmedateBackfill(portal, &priorityCounter)
			case waProto.HistorySync_FULL, waProto.HistorySync_RECENT:
				// Enqueue deferred backfills as configured.
				user.EnqueueDeferredBackfills(portal, &priorityCounter)
				user.EnqueueMediaBackfills(portal, &priorityCounter)
			}
		}

		// Tell the queue to check for new backfill requests.
		reCheckQueue <- true
	}
}

func getConversationTimestamp(conv *waProto.Conversation) uint64 {
	convTs := conv.GetConversationTimestamp()
	if convTs == 0 && len(conv.GetMessages()) > 0 {
		convTs = conv.Messages[0].GetMessage().GetMessageTimestamp()
	}
	return convTs
}

func (user *User) EnqueueImmedateBackfill(portal *Portal, priorityCounter *int) {
	maxMessages := user.bridge.Config.Bridge.HistorySync.Immediate.MaxEvents
	initialBackfill := user.bridge.DB.BackfillQuery.NewWithValues(user.MXID, database.BackfillImmediate, *priorityCounter, &portal.Key, nil, nil, maxMessages, maxMessages, 0)
	initialBackfill.Insert()
	*priorityCounter++
}

func (user *User) EnqueueDeferredBackfills(portal *Portal, priorityCounter *int) {
	for _, backfillStage := range user.bridge.Config.Bridge.HistorySync.Deferred {
		var startDate *time.Time = nil
		if backfillStage.StartDaysAgo > 0 {
			startDaysAgo := time.Now().AddDate(0, 0, -backfillStage.StartDaysAgo)
			startDate = &startDaysAgo
		}
		backfillMessages := user.bridge.DB.BackfillQuery.NewWithValues(
			user.MXID, database.BackfillDeferred, *priorityCounter, &portal.Key, startDate, nil, backfillStage.MaxBatchEvents, -1, backfillStage.BatchDelay)
		backfillMessages.Insert()
		*priorityCounter++
	}
}

func (user *User) EnqueueMediaBackfills(portal *Portal, priorityCounter *int) {
	for _, backfillStage := range user.bridge.Config.Bridge.HistorySync.Media {
		var startDate *time.Time = nil
		if backfillStage.StartDaysAgo > 0 {
			startDaysAgo := time.Now().AddDate(0, 0, -backfillStage.StartDaysAgo)
			startDate = &startDaysAgo
		}
		backfill := user.bridge.DB.BackfillQuery.NewWithValues(
			user.MXID, database.BackfillMedia, *priorityCounter, &portal.Key, startDate, nil, backfillStage.MaxBatchEvents, -1, backfillStage.BatchDelay)
		backfill.Insert()
		*priorityCounter++
	}
}

// endregion
// region Portal backfilling

var (
	PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
	PreBackfillDummyEvent    = event.Type{Type: "fi.mau.dummy.pre_backfill", Class: event.MessageEventType}

	BackfillEndDummyEvent = event.Type{Type: "fi.mau.dummy.backfill_end", Class: event.MessageEventType}
	HistorySyncMarker     = event.Type{Type: "org.matrix.msc2716.marker", Class: event.MessageEventType}
)

func (portal *Portal) backfill(source *User, messages []*waProto.WebMessageInfo) []id.EventID {
	var historyBatch, newBatch mautrix.ReqBatchSend
	var historyBatchInfos, newBatchInfos []*wrappedInfo

	firstMsgTimestamp := time.Unix(int64(messages[len(messages)-1].GetMessageTimestamp()), 0)

	historyBatch.StateEventsAtStart = make([]*event.Event, 0)
	newBatch.StateEventsAtStart = make([]*event.Event, 0)

	addedMembers := make(map[id.UserID]*event.MemberEventContent)
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
		historyBatch.StateEventsAtStart = append(historyBatch.StateEventsAtStart, &event.Event{
			Type:      event.StateMember,
			Sender:    portal.MainIntent().UserID,
			StateKey:  &mxid,
			Timestamp: firstMsgTimestamp.UnixMilli(),
			Content:   event.Content{Parsed: &inviteContent},
		}, &event.Event{
			Type:      event.StateMember,
			Sender:    puppet.MXID,
			StateKey:  &mxid,
			Timestamp: firstMsgTimestamp.UnixMilli(),
			Content:   event.Content{Parsed: &content},
		})
		addedMembers[puppet.MXID] = &content
	}

	firstMessage := portal.bridge.DB.Message.GetFirstInChat(portal.Key)
	lastMessage := portal.bridge.DB.Message.GetLastInChat(portal.Key)
	var historyMaxTs, newMinTs time.Time

	if portal.FirstEventID != "" || portal.NextBatchID != "" {
		historyBatch.PrevEventID = portal.FirstEventID
		historyBatch.BatchID = portal.NextBatchID
		if firstMessage == nil && lastMessage == nil {
			historyMaxTs = time.Now()
		} else {
			historyMaxTs = firstMessage.Timestamp
		}
	}
	if lastMessage != nil {
		newBatch.PrevEventID = lastMessage.MXID
		newMinTs = lastMessage.Timestamp
	}

	portal.log.Infofln("Processing history sync with %d messages", len(messages))
	// The messages are ordered newest to oldest, so iterate them in reverse order.
	for i := len(messages) - 1; i >= 0; i-- {
		webMsg := messages[i]
		msgType := getMessageType(webMsg.GetMessage())
		if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
			if msgType != "ignore" {
				portal.log.Debugfln("Skipping message %s with unknown type in backfill", webMsg.GetKey().GetId())
			}
			continue
		}
		info := portal.parseWebMessageInfo(source, webMsg)
		if info == nil {
			continue
		}
		var batch *mautrix.ReqBatchSend
		var infos *[]*wrappedInfo
		if !historyMaxTs.IsZero() && info.Timestamp.Before(historyMaxTs) {
			batch, infos = &historyBatch, &historyBatchInfos
		} else if !newMinTs.IsZero() && info.Timestamp.After(newMinTs) {
			batch, infos = &newBatch, &newBatchInfos
		} else {
			continue
		}
		if webMsg.GetPushName() != "" && webMsg.GetPushName() != "-" {
			existingContact, _ := source.Client.Store.Contacts.GetContact(info.Sender)
			if !existingContact.Found || existingContact.PushName == "" {
				changed, _, err := source.Client.Store.Contacts.PutPushName(info.Sender, webMsg.GetPushName())
				if err != nil {
					source.log.Errorfln("Failed to save push name of %s from historical message in device store: %v", info.Sender, err)
				} else if changed {
					source.log.Debugfln("Got push name %s for %s from historical message", webMsg.GetPushName(), info.Sender)
				}
			}
		}
		puppet := portal.getMessagePuppet(source, info)
		intent := puppet.IntentFor(portal)
		if intent.IsCustomPuppet && !portal.bridge.Config.CanDoublePuppetBackfill(puppet.CustomMXID) {
			intent = puppet.DefaultIntent()
		}
		converted := portal.convertMessage(intent, source, info, webMsg.GetMessage())
		if converted == nil {
			portal.log.Debugfln("Skipping unsupported message %s in backfill", info.ID)
			continue
		}
		if !intent.IsCustomPuppet && !portal.bridge.StateStore.IsInRoom(portal.MXID, puppet.MXID) {
			addMember(puppet)
		}
		// TODO this won't work for history
		if len(converted.ReplyTo) > 0 {
			portal.SetReply(converted.Content, converted.ReplyTo)
		}
		err := portal.appendBatchEvents(converted, info, webMsg.GetEphemeralStartTimestamp(), &batch.Events, infos)
		if err != nil {
			portal.log.Errorfln("Error handling message %s during backfill: %v", info.ID, err)
		}
	}

	if (len(historyBatch.Events) > 0 && len(historyBatch.BatchID) == 0) || len(newBatch.Events) > 0 {
		portal.log.Debugln("Sending a dummy event to avoid forward extremity errors with backfill")
		_, err := portal.MainIntent().SendMessageEvent(portal.MXID, PreBackfillDummyEvent, struct{}{})
		if err != nil {
			portal.log.Warnln("Error sending pre-backfill dummy event:", err)
		}
	}

	var insertionEventIds []id.EventID

	if len(historyBatch.Events) > 0 && len(historyBatch.PrevEventID) > 0 {
		portal.log.Infofln("Sending %d historical messages...", len(historyBatch.Events))
		historyResp, err := portal.MainIntent().BatchSend(portal.MXID, &historyBatch)
		if err != nil {
			portal.log.Errorln("Error sending batch of historical messages:", err)
		} else {
			insertionEventIds = append(insertionEventIds, historyResp.BaseInsertionEventID)
			portal.finishBatch(historyResp.EventIDs, historyBatchInfos)
			portal.NextBatchID = historyResp.NextBatchID
			portal.Update()
		}
	}

	if len(newBatch.Events) > 0 && len(newBatch.PrevEventID) > 0 {
		portal.log.Infofln("Sending %d new messages...", len(newBatch.Events))
		newResp, err := portal.MainIntent().BatchSend(portal.MXID, &newBatch)
		if err != nil {
			portal.log.Errorln("Error sending batch of new messages:", err)
		} else {
			insertionEventIds = append(insertionEventIds, newResp.BaseInsertionEventID)
			portal.finishBatch(newResp.EventIDs, newBatchInfos)
		}
	}

	return insertionEventIds
}

func (portal *Portal) parseWebMessageInfo(source *User, webMsg *waProto.WebMessageInfo) *types.MessageInfo {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     portal.Key.JID,
			IsFromMe: webMsg.GetKey().GetFromMe(),
			IsGroup:  portal.Key.JID.Server == types.GroupServer,
		},
		ID:        webMsg.GetKey().GetId(),
		PushName:  webMsg.GetPushName(),
		Timestamp: time.Unix(int64(webMsg.GetMessageTimestamp()), 0),
	}
	var err error
	if info.IsFromMe {
		info.Sender = source.JID.ToNonAD()
	} else if portal.IsPrivateChat() {
		info.Sender = portal.Key.JID
	} else if webMsg.GetParticipant() != "" {
		info.Sender, err = types.ParseJID(webMsg.GetParticipant())
	} else if webMsg.GetKey().GetParticipant() != "" {
		info.Sender, err = types.ParseJID(webMsg.GetKey().GetParticipant())
	}
	if info.Sender.IsEmpty() {
		portal.log.Warnfln("Failed to get sender of message %s (parse error: %v)", info.ID, err)
		return nil
	}
	return &info
}

func (portal *Portal) appendBatchEvents(converted *ConvertedMessage, info *types.MessageInfo, expirationStart uint64, eventsArray *[]*event.Event, infoArray *[]*wrappedInfo) error {
	mainEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Content, converted.Extra)
	if err != nil {
		return err
	}
	if converted.Caption != nil {
		captionEvt, err := portal.wrapBatchEvent(info, converted.Intent, converted.Type, converted.Caption, nil)
		if err != nil {
			return err
		}
		*eventsArray = append(*eventsArray, mainEvt, captionEvt)
		*infoArray = append(*infoArray, &wrappedInfo{info, database.MsgNormal, converted.Error, expirationStart, converted.ExpiresIn}, nil)
	} else {
		*eventsArray = append(*eventsArray, mainEvt)
		*infoArray = append(*infoArray, &wrappedInfo{info, database.MsgNormal, converted.Error, expirationStart, converted.ExpiresIn})
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

const backfillIDField = "fi.mau.whatsapp.backfill_msg_id"

func (portal *Portal) wrapBatchEvent(info *types.MessageInfo, intent *appservice.IntentAPI, eventType event.Type, content *event.MessageEventContent, extraContent map[string]interface{}) (*event.Event, error) {
	if extraContent == nil {
		extraContent = map[string]interface{}{}
	}
	extraContent[backfillIDField] = info.ID
	if intent.IsCustomPuppet {
		extraContent[doublePuppetKey] = doublePuppetValue
	}
	wrappedContent := event.Content{
		Parsed: content,
		Raw:    extraContent,
	}
	newEventType, err := portal.encrypt(&wrappedContent, eventType)
	if err != nil {
		return nil, err
	}

	if newEventType == event.EventEncrypted {
		// Clear other custom keys if the event was encrypted, but keep the double puppet identifier
		wrappedContent.Raw = map[string]interface{}{backfillIDField: info.ID}
		if intent.IsCustomPuppet {
			wrappedContent.Raw[doublePuppetKey] = doublePuppetValue
		}
	}

	return &event.Event{
		Sender:    intent.UserID,
		Type:      newEventType,
		Timestamp: info.Timestamp.UnixMilli(),
		Content:   wrappedContent,
	}, nil
}

func (portal *Portal) finishBatch(eventIDs []id.EventID, infos []*wrappedInfo) {
	if len(eventIDs) != len(infos) {
		portal.log.Errorfln("Length of event IDs (%d) and message infos (%d) doesn't match! Using slow path for mapping event IDs", len(eventIDs), len(infos))
		infoMap := make(map[types.MessageID]*wrappedInfo, len(infos))
		for _, info := range infos {
			infoMap[info.ID] = info
		}
		for _, eventID := range eventIDs {
			if evt, err := portal.MainIntent().GetEvent(portal.MXID, eventID); err != nil {
				portal.log.Warnfln("Failed to get event %s to register it in the database: %v", eventID, err)
			} else if msgID, ok := evt.Content.Raw[backfillIDField].(string); !ok {
				portal.log.Warnfln("Event %s doesn't include the WhatsApp message ID", eventID)
			} else if info, ok := infoMap[types.MessageID(msgID)]; !ok {
				portal.log.Warnfln("Didn't find info of message %s (event %s) to register it in the database", msgID, eventID)
			} else {
				portal.finishBatchEvt(info, eventID)
			}
		}
	} else {
		for i := 0; i < len(infos); i++ {
			portal.finishBatchEvt(infos[i], eventIDs[i])
		}
		portal.log.Infofln("Successfully sent %d events", len(eventIDs))
	}
}

func (portal *Portal) finishBatchEvt(info *wrappedInfo, eventID id.EventID) {
	if info == nil {
		return
	}

	portal.markHandled(nil, info.MessageInfo, eventID, true, false, info.Type, info.Error)
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

func (portal *Portal) sendPostBackfillDummy(lastTimestamp time.Time, insertionEventId id.EventID) {
	// TODO remove after clients stop using this
	_, _ = portal.MainIntent().SendMessageEvent(portal.MXID, BackfillEndDummyEvent, struct{}{})

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
	msg.Insert()
}

// endregion
