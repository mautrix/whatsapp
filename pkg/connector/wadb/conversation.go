package wadb

import (
	"context"
	"database/sql"
	"time"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type ConversationQuery struct {
	BridgeID networkid.BridgeID
	*dbutil.QueryHelper[*Conversation]
}

type Conversation struct {
	BridgeID                  networkid.BridgeID
	UserLoginID               networkid.UserLoginID
	ChatJID                   types.JID
	LastMessageTimestamp      time.Time
	Archived                  *bool
	Pinned                    *bool
	MuteEndTime               time.Time
	EndOfHistoryTransferType  *waHistorySync.Conversation_EndOfHistoryTransferType
	EphemeralExpiration       *uint32
	EphemeralSettingTimestamp *int64
	MarkedAsUnread            *bool
	UnreadCount               *uint32
}

func parseHistoryTime(ts *uint64) time.Time {
	if ts == nil || *ts == 0 {
		return time.Time{}
	}
	return time.Unix(int64(*ts), 0)
}

func NewConversation(loginID networkid.UserLoginID, chatJID types.JID, conv *waHistorySync.Conversation, mostRecentMessage time.Time) *Conversation {
	var pinned *bool
	if conv.Pinned != nil {
		pinned = ptr.Ptr(*conv.Pinned > 0)
	}
	lastMessageTS := parseHistoryTime(conv.LastMsgTimestamp)
	if lastMessageTS.IsZero() {
		lastMessageTS = mostRecentMessage
	}
	return &Conversation{
		UserLoginID:               loginID,
		ChatJID:                   chatJID,
		LastMessageTimestamp:      lastMessageTS,
		Archived:                  conv.Archived,
		Pinned:                    pinned,
		MuteEndTime:               parseHistoryTime(conv.MuteEndTime),
		EndOfHistoryTransferType:  conv.EndOfHistoryTransferType,
		EphemeralExpiration:       conv.EphemeralExpiration,
		EphemeralSettingTimestamp: conv.EphemeralSettingTimestamp,
		MarkedAsUnread:            conv.MarkedAsUnread,
		UnreadCount:               conv.UnreadCount,
	}
}

const (
	upsertHistorySyncConversationQuery = `
		INSERT INTO whatsapp_history_sync_conversation (
			bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
			end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread,
			unread_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (bridge_id, user_login_id, chat_jid)
		DO UPDATE SET
			last_message_timestamp=CASE
				WHEN whatsapp_history_sync_conversation.last_message_timestamp IS NULL
				         OR excluded.last_message_timestamp > whatsapp_history_sync_conversation.last_message_timestamp
					THEN excluded.last_message_timestamp
				ELSE whatsapp_history_sync_conversation.last_message_timestamp
			END,
			archived=COALESCE(excluded.archived, whatsapp_history_sync_conversation.archived),
			pinned=COALESCE(excluded.pinned, whatsapp_history_sync_conversation.pinned),
			mute_end_time=COALESCE(excluded.mute_end_time, whatsapp_history_sync_conversation.mute_end_time),
			end_of_history_transfer_type=COALESCE(excluded.end_of_history_transfer_type, whatsapp_history_sync_conversation.end_of_history_transfer_type),
			ephemeral_expiration=COALESCE(excluded.ephemeral_expiration, whatsapp_history_sync_conversation.ephemeral_expiration),
			ephemeral_setting_timestamp=COALESCE(excluded.ephemeral_setting_timestamp, whatsapp_history_sync_conversation.ephemeral_setting_timestamp),
			marked_as_unread=COALESCE(excluded.marked_as_unread, whatsapp_history_sync_conversation.marked_as_unread),
			unread_count=COALESCE(excluded.unread_count, whatsapp_history_sync_conversation.unread_count)
	`
	getRecentConversations = `
		SELECT
			bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
			end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread,
			unread_count
		FROM whatsapp_history_sync_conversation
		WHERE bridge_id=$1 AND user_login_id=$2 AND (synced_login_ts IS NULL OR synced_login_ts < $4)
		ORDER BY last_message_timestamp DESC
		LIMIT $3
	`
	getConversationByJID = `
		SELECT
			bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
			end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread,
			unread_count
		FROM whatsapp_history_sync_conversation
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
	`
	deleteAllConversationsQuery = "DELETE FROM whatsapp_history_sync_conversation WHERE bridge_id=$1 AND user_login_id=$2"
	deleteConversationQuery     = `
		DELETE FROM whatsapp_history_sync_conversation
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
	`
	markConversationSynced = `
		UPDATE whatsapp_history_sync_conversation
		SET synced_login_ts=$4
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
	`
)

func (cq *ConversationQuery) Put(ctx context.Context, conv *Conversation) error {
	conv.BridgeID = cq.BridgeID
	return cq.Exec(ctx, upsertHistorySyncConversationQuery, conv.sqlVariables()...)
}

func (cq *ConversationQuery) GetRecent(
	ctx context.Context, loginID networkid.UserLoginID, limit int, notSyncedAfter jsontime.Unix,
) ([]*Conversation, error) {
	limitPtr := &limit
	// Negative limit on SQLite means unlimited, but Postgres prefers a NULL limit.
	if limit < 0 && cq.GetDB().Dialect == dbutil.Postgres {
		limitPtr = nil
	}
	return cq.QueryMany(ctx, getRecentConversations, cq.BridgeID, loginID, limitPtr, notSyncedAfter)
}

func (cq *ConversationQuery) MarkSynced(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID, loginTS jsontime.Unix) error {
	return cq.Exec(ctx, markConversationSynced, cq.BridgeID, loginID, chatJID, loginTS)
}

func (cq *ConversationQuery) Get(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID) (*Conversation, error) {
	return cq.QueryOne(ctx, getConversationByJID, cq.BridgeID, loginID, chatJID)
}

func (cq *ConversationQuery) DeleteAll(ctx context.Context, loginID networkid.UserLoginID) error {
	return cq.Exec(ctx, deleteAllConversationsQuery, cq.BridgeID, loginID)
}

func (cq *ConversationQuery) Delete(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID) error {
	return cq.Exec(ctx, deleteConversationQuery, cq.BridgeID, loginID, chatJID)
}

func (c *Conversation) sqlVariables() []any {
	var lastMessageTS, muteEndTime *int64
	if !c.LastMessageTimestamp.IsZero() {
		lastMessageTS = ptr.Ptr(c.LastMessageTimestamp.Unix())
	}
	if !c.MuteEndTime.IsZero() {
		muteEndTime = ptr.Ptr(c.MuteEndTime.Unix())
	}
	return []any{
		c.BridgeID,
		c.UserLoginID,
		c.ChatJID,
		lastMessageTS,
		c.Archived,
		c.Pinned,
		muteEndTime,
		c.EndOfHistoryTransferType,
		c.EphemeralExpiration,
		c.EphemeralSettingTimestamp,
		c.MarkedAsUnread,
		c.UnreadCount,
	}
}

func (c *Conversation) Scan(row dbutil.Scannable) (*Conversation, error) {
	var lastMessageTS, muteEndTime sql.NullInt64
	err := row.Scan(
		&c.BridgeID,
		&c.UserLoginID,
		&c.ChatJID,
		&lastMessageTS,
		&c.Archived,
		&c.Pinned,
		&muteEndTime,
		&c.EndOfHistoryTransferType,
		&c.EphemeralExpiration,
		&c.EphemeralSettingTimestamp,
		&c.MarkedAsUnread,
		&c.UnreadCount,
	)
	if err != nil {
		return nil, err
	}
	if lastMessageTS.Int64 != 0 {
		c.LastMessageTimestamp = time.Unix(lastMessageTS.Int64, 0)
	}
	if muteEndTime.Int64 != 0 {
		c.MuteEndTime = time.Unix(muteEndTime.Int64, 0)
	}
	return c, nil
}
