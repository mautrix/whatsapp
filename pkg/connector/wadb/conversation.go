package wadb

import (
	"context"
	"time"

	"go.mau.fi/util/dbutil"
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
	Archived                  bool
	Pinned                    bool
	MuteEndTime               time.Time
	EndOfHistoryTransferType  waHistorySync.Conversation_EndOfHistoryTransferType
	EphemeralExpiration       time.Duration
	EphemeralSettingTimestamp int64
	MarkedAsUnread            bool
	UnreadCount               uint32
}

const (
	upsertHistorySyncConversationQuery = `
		INSERT INTO whatsapp_history_sync_conversation (
			bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
			end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread, unread_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (bridge_id, user_login_id, chat_jid)
		DO UPDATE SET
			last_message_timestamp=CASE
				WHEN excluded.last_message_timestamp > whatsapp_history_sync_conversation.last_message_timestamp THEN excluded.last_message_timestamp
				ELSE whatsapp_history_sync_conversation.last_message_timestamp
			END,
			ephemeral_expiration=COALESCE(excluded.ephemeral_expiration, whatsapp_history_sync_conversation.ephemeral_expiration),
			ephemeral_setting_timestamp=COALESCE(excluded.ephemeral_setting_timestamp, whatsapp_history_sync_conversation.ephemeral_setting_timestamp),
			end_of_history_transfer_type=excluded.end_of_history_transfer_type
	`
	getRecentConversations = `
		SELECT
			bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
			end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread, unread_count
		FROM whatsapp_history_sync_conversation
		WHERE bridge_id=$1 AND user_login_id=$2
		ORDER BY last_message_timestamp DESC
		LIMIT $3
	`
	getConversationByJID = `
		SELECT
			bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
			end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread, unread_count
		FROM whatsapp_history_sync_conversation
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
	`
	deleteAllConversationsQuery = "DELETE FROM whatsapp_history_sync_conversation WHERE bridge_id=$1 AND user_login_id=$2"
	deleteConversationQuery     = `
		DELETE FROM whatsapp_history_sync_conversation
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
	`
)

func (cq *ConversationQuery) Put(ctx context.Context, conv *Conversation) error {
	conv.BridgeID = cq.BridgeID
	return cq.Exec(ctx, upsertHistorySyncConversationQuery, conv.sqlVariables()...)
}

func (cq *ConversationQuery) GetRecent(ctx context.Context, loginID networkid.UserLoginID, limit int) ([]*Conversation, error) {
	limitPtr := &limit
	// Negative limit on SQLite means unlimited, but Postgres prefers a NULL limit.
	if limit < 0 && cq.GetDB().Dialect == dbutil.Postgres {
		limitPtr = nil
	}
	return cq.QueryMany(ctx, getRecentConversations, cq.BridgeID, loginID, limitPtr)
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
	return []any{
		c.BridgeID,
		c.UserLoginID,
		c.ChatJID,
		c.LastMessageTimestamp.Unix(),
		c.Archived,
		c.Pinned,
		c.MuteEndTime.Unix(),
		c.EndOfHistoryTransferType,
		int64(c.EphemeralExpiration.Seconds()),
		c.EphemeralSettingTimestamp,
		c.MarkedAsUnread,
		c.UnreadCount,
	}
}

func (c *Conversation) Scan(row dbutil.Scannable) (*Conversation, error) {
	var lastMessageTS, muteEndTime, ephemeralExpiration int64
	err := row.Scan(
		&c.BridgeID,
		&c.UserLoginID,
		&c.ChatJID,
		&lastMessageTS,
		&c.Archived,
		&c.Pinned,
		&muteEndTime,
		&c.EndOfHistoryTransferType,
		&ephemeralExpiration,
		&c.EphemeralSettingTimestamp,
		&c.MarkedAsUnread,
		&c.UnreadCount,
	)
	if err != nil {
		return nil, err
	}
	if lastMessageTS != 0 {
		c.LastMessageTimestamp = time.Unix(lastMessageTS, 0)
	}
	if muteEndTime != 0 {
		c.MuteEndTime = time.Unix(muteEndTime, 0)
	}
	c.EphemeralExpiration = time.Duration(ephemeralExpiration) * time.Second
	return c, nil
}
