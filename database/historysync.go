// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan, Sumner Evans
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

package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"

	_ "github.com/mattn/go-sqlite3"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type HistorySyncQuery struct {
	db  *Database
	log log.Logger
}

type HistorySyncConversation struct {
	db  *Database
	log log.Logger

	UserID                   id.UserID
	ConversationID           string
	PortalKey                *PortalKey
	LastMessageTimestamp     time.Time
	MuteEndTime              time.Time
	Archived                 bool
	Pinned                   uint32
	DisappearingMode         waProto.DisappearingMode_DisappearingModeInitiator
	EndOfHistoryTransferType waProto.Conversation_ConversationEndOfHistoryTransferType
	EphemeralExpiration      *uint32
	MarkedAsUnread           bool
	UnreadCount              uint32
}

func (hsq *HistorySyncQuery) NewConversation() *HistorySyncConversation {
	return &HistorySyncConversation{
		db:        hsq.db,
		log:       hsq.log,
		PortalKey: &PortalKey{},
	}
}

func (hsq *HistorySyncQuery) NewConversationWithValues(
	userID id.UserID,
	conversationID string,
	portalKey *PortalKey,
	lastMessageTimestamp,
	muteEndTime uint64,
	archived bool,
	pinned uint32,
	disappearingMode waProto.DisappearingMode_DisappearingModeInitiator,
	endOfHistoryTransferType waProto.Conversation_ConversationEndOfHistoryTransferType,
	ephemeralExpiration *uint32,
	markedAsUnread bool,
	unreadCount uint32) *HistorySyncConversation {
	return &HistorySyncConversation{
		db:                       hsq.db,
		log:                      hsq.log,
		UserID:                   userID,
		ConversationID:           conversationID,
		PortalKey:                portalKey,
		LastMessageTimestamp:     time.Unix(int64(lastMessageTimestamp), 0),
		MuteEndTime:              time.Unix(int64(muteEndTime), 0),
		Archived:                 archived,
		Pinned:                   pinned,
		DisappearingMode:         disappearingMode,
		EndOfHistoryTransferType: endOfHistoryTransferType,
		EphemeralExpiration:      ephemeralExpiration,
		MarkedAsUnread:           markedAsUnread,
		UnreadCount:              unreadCount,
	}
}

const (
	getNMostRecentConversations = `
		SELECT user_mxid, conversation_id, portal_jid, portal_receiver, last_message_timestamp, archived, pinned, mute_end_time, disappearing_mode, end_of_history_transfer_type, ephemeral_expiration, marked_as_unread, unread_count
		  FROM history_sync_conversation
		 WHERE user_mxid=$1
		 ORDER BY last_message_timestamp DESC
		 LIMIT $2
	`
	getConversationByPortal = `
		SELECT user_mxid, conversation_id, portal_jid, portal_receiver, last_message_timestamp, archived, pinned, mute_end_time, disappearing_mode, end_of_history_transfer_type, ephemeral_expiration, marked_as_unread, unread_count
		  FROM history_sync_conversation
		 WHERE user_mxid=$1
		   AND portal_jid=$2
		   AND portal_receiver=$3
	`
)

func (hsc *HistorySyncConversation) Upsert() {
	_, err := hsc.db.Exec(`
		INSERT INTO history_sync_conversation (user_mxid, conversation_id, portal_jid, portal_receiver, last_message_timestamp, archived, pinned, mute_end_time, disappearing_mode, end_of_history_transfer_type, ephemeral_expiration, marked_as_unread, unread_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (user_mxid, conversation_id)
		DO UPDATE SET
			portal_jid=EXCLUDED.portal_jid,
			portal_receiver=EXCLUDED.portal_receiver,
			last_message_timestamp=CASE
				WHEN EXCLUDED.last_message_timestamp > history_sync_conversation.last_message_timestamp THEN EXCLUDED.last_message_timestamp
				ELSE history_sync_conversation.last_message_timestamp
			END,
			archived=EXCLUDED.archived,
			pinned=EXCLUDED.pinned,
			mute_end_time=EXCLUDED.mute_end_time,
			disappearing_mode=EXCLUDED.disappearing_mode,
			end_of_history_transfer_type=EXCLUDED.end_of_history_transfer_type,
			ephemeral_expiration=EXCLUDED.ephemeral_expiration,
			marked_as_unread=EXCLUDED.marked_as_unread,
			unread_count=EXCLUDED.unread_count
	`,
		hsc.UserID,
		hsc.ConversationID,
		hsc.PortalKey.JID.String(),
		hsc.PortalKey.Receiver.String(),
		hsc.LastMessageTimestamp,
		hsc.Archived,
		hsc.Pinned,
		hsc.MuteEndTime,
		hsc.DisappearingMode,
		hsc.EndOfHistoryTransferType,
		hsc.EphemeralExpiration,
		hsc.MarkedAsUnread,
		hsc.UnreadCount)
	if err != nil {
		hsc.log.Warnfln("Failed to insert history sync conversation %s/%s: %v", hsc.UserID, hsc.ConversationID, err)
	}
}

func (hsc *HistorySyncConversation) Scan(row Scannable) *HistorySyncConversation {
	err := row.Scan(
		&hsc.UserID,
		&hsc.ConversationID,
		&hsc.PortalKey.JID,
		&hsc.PortalKey.Receiver,
		&hsc.LastMessageTimestamp,
		&hsc.Archived,
		&hsc.Pinned,
		&hsc.MuteEndTime,
		&hsc.DisappearingMode,
		&hsc.EndOfHistoryTransferType,
		&hsc.EphemeralExpiration,
		&hsc.MarkedAsUnread,
		&hsc.UnreadCount)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			hsc.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return hsc
}

func (hsq *HistorySyncQuery) GetNMostRecentConversations(userID id.UserID, n int) (conversations []*HistorySyncConversation) {
	nPtr := &n
	// Negative limit on SQLite means unlimited, but Postgres prefers a NULL limit.
	if n < 0 && hsq.db.dialect == "postgres" {
		nPtr = nil
	}
	rows, err := hsq.db.Query(getNMostRecentConversations, userID, nPtr)
	defer rows.Close()
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		conversations = append(conversations, hsq.NewConversation().Scan(rows))
	}
	return
}

func (hsq *HistorySyncQuery) GetConversation(userID id.UserID, portalKey *PortalKey) (conversation *HistorySyncConversation) {
	rows, err := hsq.db.Query(getConversationByPortal, userID, portalKey.JID, portalKey.Receiver)
	defer rows.Close()
	if err != nil || rows == nil {
		return nil
	}
	if rows.Next() {
		conversation = hsq.NewConversation().Scan(rows)
	}
	return
}

func (hsq *HistorySyncQuery) DeleteAllConversations(userID id.UserID) error {
	_, err := hsq.db.Exec("DELETE FROM history_sync_conversation WHERE user_mxid=$1", userID)
	return err
}

const (
	getMessagesBetween = `
		SELECT data FROM history_sync_message
		WHERE user_mxid=$1 AND conversation_id=$2
			%s
		ORDER BY timestamp DESC
		%s
	`
	deleteMessagesBetweenExclusive = `
		DELETE FROM history_sync_message
		WHERE user_mxid=$1 AND conversation_id=$2 AND timestamp<$3 AND timestamp>$4
	`
)

type HistorySyncMessage struct {
	db  *Database
	log log.Logger

	UserID         id.UserID
	ConversationID string
	MessageID      string
	Timestamp      time.Time
	Data           []byte
}

func (hsq *HistorySyncQuery) NewMessageWithValues(userID id.UserID, conversationID, messageID string, message *waProto.HistorySyncMsg) (*HistorySyncMessage, error) {
	msgData, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	return &HistorySyncMessage{
		db:             hsq.db,
		log:            hsq.log,
		UserID:         userID,
		ConversationID: conversationID,
		MessageID:      messageID,
		Timestamp:      time.Unix(int64(message.Message.GetMessageTimestamp()), 0),
		Data:           msgData,
	}, nil
}

func (hsm *HistorySyncMessage) Insert() {
	_, err := hsm.db.Exec(`
		INSERT INTO history_sync_message (user_mxid, conversation_id, message_id, timestamp, data, inserted_time)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_mxid, conversation_id, message_id) DO NOTHING
	`, hsm.UserID, hsm.ConversationID, hsm.MessageID, hsm.Timestamp, hsm.Data, time.Now())
	if err != nil {
		hsm.log.Warnfln("Failed to insert history sync message %s/%s: %v", hsm.ConversationID, hsm.Timestamp, err)
	}
}

func (hsq *HistorySyncQuery) GetMessagesBetween(userID id.UserID, conversationID string, startTime, endTime *time.Time, limit int) (messages []*waProto.WebMessageInfo) {
	whereClauses := ""
	args := []interface{}{userID, conversationID}
	argNum := 3
	if startTime != nil {
		whereClauses += fmt.Sprintf(" AND timestamp >= $%d", argNum)
		args = append(args, startTime)
		argNum++
	}
	if endTime != nil {
		whereClauses += fmt.Sprintf(" AND timestamp <= $%d", argNum)
		args = append(args, endTime)
	}

	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("LIMIT %d", limit)
	}

	rows, err := hsq.db.Query(fmt.Sprintf(getMessagesBetween, whereClauses, limitClause), args...)
	defer rows.Close()
	if err != nil || rows == nil {
		return nil
	}

	var msgData []byte
	for rows.Next() {
		err = rows.Scan(&msgData)
		if err != nil {
			hsq.log.Errorfln("Database scan failed: %v", err)
			continue
		}
		var historySyncMsg waProto.HistorySyncMsg
		err = proto.Unmarshal(msgData, &historySyncMsg)
		if err != nil {
			hsq.log.Errorfln("Failed to unmarshal history sync message: %v", err)
			continue
		}
		messages = append(messages, historySyncMsg.Message)
	}
	return
}

func (hsq *HistorySyncQuery) DeleteMessages(userID id.UserID, conversationID string, messages []*waProto.WebMessageInfo) error {
	newest := messages[0]
	beforeTS := time.Unix(int64(newest.GetMessageTimestamp())+1, 0)
	oldest := messages[len(messages)-1]
	afterTS := time.Unix(int64(oldest.GetMessageTimestamp())-1, 0)
	_, err := hsq.db.Exec(deleteMessagesBetweenExclusive, userID, conversationID, beforeTS, afterTS)
	return err
}

func (hsq *HistorySyncQuery) DeleteAllMessages(userID id.UserID) error {
	_, err := hsq.db.Exec("DELETE FROM history_sync_message WHERE user_mxid=$1", userID)
	return err
}

func (hsq *HistorySyncQuery) DeleteAllMessagesForPortal(userID id.UserID, portalKey PortalKey) error {
	_, err := hsq.db.Exec(`
		DELETE FROM history_sync_message
		WHERE user_mxid=$1
			AND portal_jid=$2
			AND portal_receiver=$3
	`, userID, portalKey.JID, portalKey.Receiver)
	return err
}
