// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan, Sumner Evans
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
	"context"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/id"
)

type HistorySyncQuery struct {
	*dbutil.QueryHelper[*HistorySyncConversation]
}

type HistorySyncConversation struct {
	qh *dbutil.QueryHelper[*HistorySyncConversation]

	UserID                   id.UserID
	ConversationID           string
	PortalKey                PortalKey
	LastMessageTimestamp     time.Time
	MuteEndTime              time.Time
	Archived                 bool
	Pinned                   uint32
	DisappearingMode         waProto.DisappearingMode_Initiator
	EndOfHistoryTransferType waProto.Conversation_EndOfHistoryTransferType
	EphemeralExpiration      *uint32
	MarkedAsUnread           bool
	UnreadCount              uint32
}

func newHistorySyncConversation(qh *dbutil.QueryHelper[*HistorySyncConversation]) *HistorySyncConversation {
	return &HistorySyncConversation{
		qh: qh,
	}
}

func (hsq *HistorySyncQuery) NewConversationWithValues(
	userID id.UserID,
	conversationID string,
	portalKey PortalKey,
	lastMessageTimestamp,
	muteEndTime uint64,
	archived bool,
	pinned uint32,
	disappearingMode waProto.DisappearingMode_Initiator,
	endOfHistoryTransferType waProto.Conversation_EndOfHistoryTransferType,
	ephemeralExpiration *uint32,
	markedAsUnread bool,
	unreadCount uint32,
) *HistorySyncConversation {
	return &HistorySyncConversation{
		qh:                       hsq.QueryHelper,
		UserID:                   userID,
		ConversationID:           conversationID,
		PortalKey:                portalKey,
		LastMessageTimestamp:     time.Unix(int64(lastMessageTimestamp), 0).UTC(),
		MuteEndTime:              time.Unix(int64(muteEndTime), 0).UTC(),
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
	upsertHistorySyncConversationQuery = `
		INSERT INTO history_sync_conversation (user_mxid, conversation_id, portal_jid, portal_receiver, last_message_timestamp, archived, pinned, mute_end_time, disappearing_mode, end_of_history_transfer_type, ephemeral_expiration, marked_as_unread, unread_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (user_mxid, conversation_id)
		DO UPDATE SET
			last_message_timestamp=CASE
				WHEN EXCLUDED.last_message_timestamp > history_sync_conversation.last_message_timestamp THEN EXCLUDED.last_message_timestamp
				ELSE history_sync_conversation.last_message_timestamp
			END,
			end_of_history_transfer_type=EXCLUDED.end_of_history_transfer_type
	`
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
	deleteAllConversationsQuery        = "DELETE FROM history_sync_conversation WHERE user_mxid=$1"
	deleteHistorySyncConversationQuery = `
		DELETE FROM history_sync_conversation
		WHERE user_mxid=$1 AND conversation_id=$2
	`
)

func (hsc *HistorySyncConversation) sqlVariables() []any {
	return []any{
		hsc.UserID,
		hsc.ConversationID,
		hsc.PortalKey.JID,
		hsc.PortalKey.Receiver,
		hsc.LastMessageTimestamp,
		hsc.Archived,
		hsc.Pinned,
		hsc.MuteEndTime,
		hsc.DisappearingMode,
		hsc.EndOfHistoryTransferType,
		hsc.EphemeralExpiration,
		hsc.MarkedAsUnread,
		hsc.UnreadCount,
	}
}

func (hsc *HistorySyncConversation) Upsert(ctx context.Context) error {
	return hsc.qh.Exec(ctx, upsertHistorySyncConversationQuery, hsc.sqlVariables()...)
}

func (hsc *HistorySyncConversation) Scan(row dbutil.Scannable) (*HistorySyncConversation, error) {
	return dbutil.ValueOrErr(hsc, row.Scan(
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
		&hsc.UnreadCount,
	))
}

func (hsq *HistorySyncQuery) GetRecentConversations(ctx context.Context, userID id.UserID, n int) ([]*HistorySyncConversation, error) {
	nPtr := &n
	// Negative limit on SQLite means unlimited, but Postgres prefers a NULL limit.
	if n < 0 && hsq.GetDB().Dialect == dbutil.Postgres {
		nPtr = nil
	}
	return hsq.QueryMany(ctx, getNMostRecentConversations, userID, nPtr)
}

func (hsq *HistorySyncQuery) GetConversation(ctx context.Context, userID id.UserID, portalKey PortalKey) (*HistorySyncConversation, error) {
	return hsq.QueryOne(ctx, getConversationByPortal, userID, portalKey.JID, portalKey.Receiver)
}

func (hsq *HistorySyncQuery) DeleteAllConversations(ctx context.Context, userID id.UserID) error {
	return hsq.Exec(ctx, deleteAllConversationsQuery, userID)
}

const (
	insertHistorySyncMessageQuery = `
		INSERT INTO history_sync_message (user_mxid, conversation_id, message_id, timestamp, data, inserted_time)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_mxid, conversation_id, message_id) DO NOTHING
	`
	getHistorySyncMessagesBetweenQueryTemplate = `
		SELECT data FROM history_sync_message
		WHERE user_mxid=$1 AND conversation_id=$2
			%s
		ORDER BY timestamp DESC
		%s
	`
	deleteHistorySyncMessagesBetweenExclusiveQuery = `
		DELETE FROM history_sync_message
		WHERE user_mxid=$1 AND conversation_id=$2 AND timestamp<$3 AND timestamp>$4
	`
	deleteAllHistorySyncMessagesQuery       = "DELETE FROM history_sync_message WHERE user_mxid=$1"
	deleteHistorySyncMessagesForPortalQuery = `
		DELETE FROM history_sync_message
		WHERE user_mxid=$1 AND conversation_id=$2
	`
	conversationHasHistorySyncMessagesQuery = `
		SELECT EXISTS(
		    SELECT 1 FROM history_sync_message
			WHERE user_mxid=$1 AND conversation_id=$2
		)
	`
)

type HistorySyncMessage struct {
	hsq *HistorySyncQuery

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
		hsq: hsq,

		UserID:         userID,
		ConversationID: conversationID,
		MessageID:      messageID,
		Timestamp:      time.Unix(int64(message.Message.GetMessageTimestamp()), 0),
		Data:           msgData,
	}, nil
}

func (hsm *HistorySyncMessage) Insert(ctx context.Context) error {
	return hsm.hsq.Exec(ctx, insertHistorySyncMessageQuery, hsm.UserID, hsm.ConversationID, hsm.MessageID, hsm.Timestamp, hsm.Data, time.Now())
}

func scanWebMessageInfo(rows dbutil.Scannable) (*waProto.WebMessageInfo, error) {
	var msgData []byte
	err := rows.Scan(&msgData)
	if err != nil {
		return nil, err
	}
	var historySyncMsg waProto.HistorySyncMsg
	err = proto.Unmarshal(msgData, &historySyncMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return historySyncMsg.GetMessage(), nil
}

func (hsq *HistorySyncQuery) GetMessagesBetween(ctx context.Context, userID id.UserID, conversationID string, startTime, endTime *time.Time, limit int) ([]*waProto.WebMessageInfo, error) {
	whereClauses := ""
	args := []any{userID, conversationID}
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
	query := fmt.Sprintf(getHistorySyncMessagesBetweenQueryTemplate, whereClauses, limitClause)

	return dbutil.ConvertRowFn[*waProto.WebMessageInfo](scanWebMessageInfo).
		NewRowIter(hsq.GetDB().Query(ctx, query, args...)).
		AsList()
}

func (hsq *HistorySyncQuery) DeleteMessages(ctx context.Context, userID id.UserID, conversationID string, messages []*waProto.WebMessageInfo) error {
	newest := messages[0]
	beforeTS := time.Unix(int64(newest.GetMessageTimestamp())+1, 0)
	oldest := messages[len(messages)-1]
	afterTS := time.Unix(int64(oldest.GetMessageTimestamp())-1, 0)
	return hsq.Exec(ctx, deleteHistorySyncMessagesBetweenExclusiveQuery, userID, conversationID, beforeTS, afterTS)
}

func (hsq *HistorySyncQuery) DeleteAllMessages(ctx context.Context, userID id.UserID) error {
	return hsq.Exec(ctx, deleteAllHistorySyncMessagesQuery, userID)
}

func (hsq *HistorySyncQuery) DeleteAllMessagesForPortal(ctx context.Context, userID id.UserID, portalKey PortalKey) error {
	return hsq.Exec(ctx, deleteHistorySyncMessagesForPortalQuery, userID, portalKey.JID)
}

func (hsq *HistorySyncQuery) ConversationHasMessages(ctx context.Context, userID id.UserID, portalKey PortalKey) (exists bool, err error) {
	err = hsq.GetDB().QueryRow(ctx, conversationHasHistorySyncMessagesQuery, userID, portalKey.JID).Scan(&exists)
	return
}

func (hsq *HistorySyncQuery) DeleteConversation(ctx context.Context, userID id.UserID, jid string) error {
	// This will also clear history_sync_message as there's a foreign key constraint
	return hsq.Exec(ctx, deleteHistorySyncConversationQuery, userID, jid)
}
