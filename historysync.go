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
	"sort"
	"sync"
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

const (
	FastBackfillPortalCount  = 20
	FastBackfillMessageCount = 20
	FastBackfillMessageCap   = 30
)

type portalToBackfill struct {
	portal *Portal
	conv   *waProto.Conversation
	msgs   []*waProto.WebMessageInfo
}

type wrappedInfo struct {
	*types.MessageInfo
	Error database.MessageErrorType
}

type conversationList []*waProto.Conversation

var _ sort.Interface = (conversationList)(nil)

func (c conversationList) Len() int {
	return len(c)
}

func (c conversationList) Less(i, j int) bool {
	return getConversationTimestamp(c[i]) < getConversationTimestamp(c[j])
}

func (c conversationList) Swap(i, j int) {
	c[i], c[j] = c[j], c[i]
}

func (user *User) handleHistorySyncsLoop() {
	for evt := range user.historySyncs {
		go user.sendBridgeState(BridgeState{StateEvent: StateBackfilling})
		user.handleHistorySync(evt.Data)
		if len(user.historySyncs) == 0 && user.IsConnected() {
			go user.sendBridgeState(BridgeState{StateEvent: StateConnected})
		}
	}
}

func (user *User) handleHistorySync(evt *waProto.HistorySync) {
	if evt == nil || evt.SyncType == nil || evt.GetSyncType() == waProto.HistorySync_INITIAL_STATUS_V3 || evt.GetSyncType() == waProto.HistorySync_PUSH_NAME {
		return
	}
	user.log.Infofln("Handling history sync with type %s, %d conversations, chunk order %d, progress %d%%", evt.GetSyncType(), len(evt.GetConversations()), evt.GetChunkOrder(), evt.GetProgress())

	conversations := conversationList(evt.GetConversations())
	// We want to handle recent conversations first
	sort.Sort(sort.Reverse(conversations))
	portalsToBackfill := make(chan portalToBackfill, len(conversations))

	var backfillWait, fastBackfillWait sync.WaitGroup
	var fastBackfillWaitDoneOnce sync.Once
	// We have to add 1 to the waitgroup beforehand to make sure the wait in the goroutine doesn't finish
	// before we add the actual numbers.
	fastBackfillWait.Add(1)
	backfillWait.Add(1)
	go func() {
		// Wait for the fast parallelized backfill to complete, then start the slow backfill loop (see explanation below)
		fastBackfillWait.Wait()
		user.slowBackfillLoop(portalsToBackfill, backfillWait.Done)
	}()
	for i, conv := range conversations {
		// This will create portals and start backfilling for them.
		//
		// The first 20 (FastBackfillPortalCount) portals will be parallelized, where the portal is
		// created and recent messages are backfilled in parallel. Other portals will be created
		// synchronously (and this will only return when they're created).
		//
		// For said other portals, and older messages in the parallelized portals, backfilling also
		// happens synchronously: the portals and messages to backfill are added to the
		// portalsToBackfill channel, which is consumed one-by-one in the slowBackfillLoop method.
		// That loop is only started after the fast parallelized backfill is completed.
		user.handleHistorySyncConversation(i, conv, &fastBackfillWait, portalsToBackfill)
		if i == FastBackfillPortalCount {
			// There won't be any more portals going the fast backfill route, so remove the 1 item
			// that was added to the wait group at the beginning.
			fastBackfillWaitDoneOnce.Do(fastBackfillWait.Done)
		}
	}
	fastBackfillWaitDoneOnce.Do(fastBackfillWait.Done)
	// Wait for fast backfill to complete to make sure everything necessary is in the slow backfill queue,
	// then close the slow backfill queue and wait for the loop to finish handling the queue.
	fastBackfillWait.Wait()
	close(portalsToBackfill)
	backfillWait.Wait()
	user.log.Infofln("Finished handling history sync with type %s, %d conversations, chunk order %d, progress %d%%", evt.GetSyncType(), len(conversations), evt.GetChunkOrder(), evt.GetProgress())
}

func (user *User) slowBackfillLoop(ch chan portalToBackfill, done func()) {
	defer done()
	for ptb := range ch {
		if len(ptb.msgs) > 0 {
			user.log.Debugln("Bridging history sync payload for", ptb.portal.Key.JID)
			ptb.portal.backfill(user, ptb.msgs)
		} else {
			user.log.Debugfln("Not backfilling %s: no bridgeable messages found", ptb.portal.Key.JID)
		}
		if !ptb.conv.GetMarkedAsUnread() && ptb.conv.GetUnreadCount() == 0 {
			user.markSelfReadFull(ptb.portal)
		}
	}
}

func (user *User) handleHistorySyncConversation(index int, conv *waProto.Conversation, fastBackfillWait *sync.WaitGroup, portalsToBackfill chan portalToBackfill) {
	jid, err := types.ParseJID(conv.GetId())
	if err != nil {
		user.log.Warnfln("Failed to parse chat JID '%s' in history sync: %v", conv.GetId(), err)
		return
	}

	// Update the client store with basic chat settings.
	muteEnd := time.Unix(int64(conv.GetMuteEndTime()), 0)
	if muteEnd.After(time.Now()) {
		_ = user.Client.Store.ChatSettings.PutMutedUntil(jid, muteEnd)
	}
	if conv.GetArchived() {
		_ = user.Client.Store.ChatSettings.PutArchived(jid, true)
	}
	if conv.GetPinned() > 0 {
		_ = user.Client.Store.ChatSettings.PutPinned(jid, true)
	}

	portal := user.GetPortalByJID(jid)
	if conv.EphemeralExpiration != nil && portal.ExpirationTime != conv.GetEphemeralExpiration() {
		portal.ExpirationTime = conv.GetEphemeralExpiration()
		portal.Update()
	}
	// Check if portal is too old or doesn't contain anything we can bridge.
	if !user.shouldCreatePortalForHistorySync(conv, portal) {
		return
	}

	var msgs []*waProto.WebMessageInfo
	if user.bridge.Config.Bridge.HistorySync.Backfill {
		msgs = filterMessagesToBackfill(conv.GetMessages())
	}
	ptb := portalToBackfill{portal: portal, conv: conv, msgs: msgs}
	if len(portal.MXID) == 0 {
		// For the first few chats, do the portal creation and some backfilling in parallel to populate the chat list ASAP
		if index < FastBackfillPortalCount {
			fastBackfillWait.Add(1)
			go user.fastBackfillRoutine(ptb, fastBackfillWait.Done, portalsToBackfill)
			return
		}
		user.log.Debugln("Creating portal for", portal.Key.JID, "as part of history sync handling")
		err = portal.CreateMatrixRoom(user, getPartialInfoFromConversation(jid, conv), false)
		if err != nil {
			user.log.Warnfln("Failed to create room for %s during backfill: %v", portal.Key.JID, err)
			return
		}
	} else {
		portal.UpdateMatrixRoom(user, nil)
	}
	if !user.bridge.Config.Bridge.HistorySync.Backfill {
		user.log.Debugln("Backfill is disabled, not bridging history sync payload for", portal.Key.JID)
	} else {
		portalsToBackfill <- ptb
	}
}

func getConversationTimestamp(conv *waProto.Conversation) uint64 {
	convTs := conv.GetConversationTimestamp()
	if convTs == 0 && len(conv.GetMessages()) > 0 {
		convTs = conv.Messages[0].GetMessage().GetMessageTimestamp()
	}
	return convTs
}

func (user *User) shouldCreatePortalForHistorySync(conv *waProto.Conversation, portal *Portal) bool {
	maxAge := user.bridge.Config.Bridge.HistorySync.MaxAge
	minLastMsgToCreate := time.Now().Add(-time.Duration(maxAge) * time.Second)
	lastMsg := time.Unix(int64(getConversationTimestamp(conv)), 0)

	if len(portal.MXID) > 0 {
		user.log.Debugfln("Portal for %s already exists, ensuring user is invited", portal.Key.JID)
		portal.ensureUserInvited(user)
		// Portal exists, let backfill continue
		return true
	} else if !user.bridge.Config.Bridge.HistorySync.CreatePortals {
		user.log.Debugfln("Not creating portal for %s: creating rooms from history sync is disabled", portal.Key.JID)
	} else if !containsSupportedMessages(conv) {
		user.log.Debugfln("Not creating portal for %s: no interesting messages found", portal.Key.JID)
	} else if maxAge > 0 && !lastMsg.After(minLastMsgToCreate) {
		user.log.Debugfln("Not creating portal for %s: last message older than limit (%s)", portal.Key.JID, lastMsg)
	} else {
		// Portal doesn't exist, but should be created
		return true
	}
	// Portal shouldn't be created, reason logged above
	return false
}

func (user *User) fastBackfillRoutine(ptb portalToBackfill, done func(), slowBackfillChan chan portalToBackfill) {
	defer done()

	user.log.Debugln("Asynchronously creating portal for", ptb.portal.Key.JID, "as part of history sync handling")
	err := ptb.portal.CreateMatrixRoom(user, getPartialInfoFromConversation(ptb.portal.Key.JID, ptb.conv), false)
	if err != nil {
		user.log.Warnfln("Failed to create room for %s during backfill: %v", ptb.portal.Key.JID, err)
		return
	}

	if user.bridge.Config.Bridge.HistorySync.Backfill {
		if len(ptb.msgs) > FastBackfillMessageCap {
			user.log.Debugfln("Bridging first %d messages of history sync payload for %s (async)", FastBackfillMessageCount, ptb.portal.Key.JID)
			ptb.portal.backfill(user, ptb.msgs[:FastBackfillMessageCount])
			// Send the rest of the messages off to the slow backfill queue
			ptb.msgs = ptb.msgs[FastBackfillMessageCount:]
			slowBackfillChan <- ptb
		} else if len(ptb.msgs) > 0 {
			user.log.Debugfln("Bridging all messages (%d) of history sync payload for %s (async)", len(ptb.msgs), ptb.portal.Key.JID)
			ptb.portal.backfill(user, ptb.msgs)
		} else {
			user.log.Debugfln("Not backfilling %s: no bridgeable messages found", ptb.portal.Key.JID)
		}
	} else {
		user.log.Debugln("Backfill is disabled, not bridging history sync payload for", ptb.portal.Key.JID)
	}
}

func filterMessagesToBackfill(messages []*waProto.HistorySyncMsg) []*waProto.WebMessageInfo {
	filtered := make([]*waProto.WebMessageInfo, 0, len(messages))
	for _, msg := range messages {
		wmi := msg.GetMessage()
		msgType := getMessageType(wmi.GetMessage())
		if msgType == "unknown" || msgType == "ignore" || msgType == "unknown_protocol" {
			continue
		} else {
			filtered = append(filtered, wmi)
		}
	}
	return filtered
}

func containsSupportedMessages(conv *waProto.Conversation) bool {
	for _, msg := range conv.GetMessages() {
		if containsSupportedMessage(msg.GetMessage().GetMessage()) {
			return true
		}
	}
	return false
}

func getPartialInfoFromConversation(jid types.JID, conv *waProto.Conversation) *types.GroupInfo {
	// TODO broadcast list info?
	if jid.Server != types.GroupServer {
		return nil
	}
	participants := make([]types.GroupParticipant, len(conv.GetParticipant()))
	for i, pcp := range conv.GetParticipant() {
		participantJID, _ := types.ParseJID(pcp.GetUserJid())
		participants[i] = types.GroupParticipant{
			JID:          participantJID,
			IsAdmin:      pcp.GetRank() == waProto.GroupParticipant_ADMIN,
			IsSuperAdmin: pcp.GetRank() == waProto.GroupParticipant_SUPERADMIN,
		}
	}
	return &types.GroupInfo{
		JID:          jid,
		GroupName:    types.GroupName{Name: conv.GetName()},
		Participants: participants,
	}
}

// endregion
// region Portal backfilling

var (
	PortalCreationDummyEvent = event.Type{Type: "fi.mau.dummy.portal_created", Class: event.MessageEventType}
	BackfillDummyStateEvent  = event.Type{Type: "fi.mau.dummy.blank_backfill_state", Class: event.StateEventType}
	BackfillEndDummyEvent    = event.Type{Type: "fi.mau.dummy.backfill_end", Class: event.MessageEventType}
	PreBackfillDummyEvent    = event.Type{Type: "fi.mau.dummy.pre_backfill", Class: event.MessageEventType}
)

func (portal *Portal) backfill(source *User, messages []*waProto.WebMessageInfo) {
	portal.backfillLock.Lock()
	defer portal.backfillLock.Unlock()

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
		err := portal.appendBatchEvents(converted, info, &batch.Events, infos)
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

	if len(historyBatch.Events) > 0 && len(historyBatch.PrevEventID) > 0 {
		portal.log.Infofln("Sending %d historical messages...", len(historyBatch.Events))
		historyResp, err := portal.MainIntent().BatchSend(portal.MXID, &historyBatch)
		if err != nil {
			portal.log.Errorln("Error sending batch of historical messages:", err)
		} else {
			portal.finishBatch(historyResp.EventIDs, historyBatchInfos)
			portal.NextBatchID = historyResp.NextBatchID
			portal.Update()
			// If batchID is non-empty, it means this is backfilling very old messages, and we don't need a post-backfill dummy.
			if historyBatch.BatchID == "" {
				portal.sendPostBackfillDummy(time.UnixMilli(historyBatch.Events[len(historyBatch.Events)-1].Timestamp))
			}
		}
	}

	if len(newBatch.Events) > 0 && len(newBatch.PrevEventID) > 0 {
		portal.log.Infofln("Sending %d new messages...", len(newBatch.Events))
		newResp, err := portal.MainIntent().BatchSend(portal.MXID, &newBatch)
		if err != nil {
			portal.log.Errorln("Error sending batch of new messages:", err)
		} else {
			portal.finishBatch(newResp.EventIDs, newBatchInfos)
			portal.sendPostBackfillDummy(time.UnixMilli(newBatch.Events[len(newBatch.Events)-1].Timestamp))
		}
	}
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

func (portal *Portal) appendBatchEvents(converted *ConvertedMessage, info *types.MessageInfo, eventsArray *[]*event.Event, infoArray *[]*wrappedInfo) error {
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
		*infoArray = append(*infoArray, &wrappedInfo{info, converted.Error}, nil)
	} else {
		*eventsArray = append(*eventsArray, mainEvt)
		*infoArray = append(*infoArray, &wrappedInfo{info, converted.Error})
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
				portal.markHandled(nil, info.MessageInfo, eventID, true, false, info.Error)
			}
		}
	} else {
		for i := 0; i < len(infos); i++ {
			if infos[i] != nil {
				portal.markHandled(nil, infos[i].MessageInfo, eventIDs[i], true, false, infos[i].Error)
			}
		}
		portal.log.Infofln("Successfully sent %d events", len(eventIDs))
	}
}

func (portal *Portal) sendPostBackfillDummy(lastTimestamp time.Time) {
	resp, err := portal.MainIntent().SendMessageEvent(portal.MXID, BackfillEndDummyEvent, struct{}{})
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
