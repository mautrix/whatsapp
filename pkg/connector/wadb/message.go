package wadb

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/util/exslices"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type MessageQuery struct {
	BridgeID networkid.BridgeID
	*dbutil.Database
}

const (
	insertHistorySyncMessageQuery = `
		INSERT INTO whatsapp_history_sync_message (bridge_id, user_login_id, chat_jid, sender_jid, message_id, timestamp, data, inserted_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (bridge_id, user_login_id, chat_jid, sender_jid, message_id) DO NOTHING
	`
	getHistorySyncMessagesBetweenQueryTemplate = `
		SELECT data FROM whatsapp_history_sync_message
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
			%s
		ORDER BY timestamp DESC
		%s
	`
	deleteHistorySyncMessagesBetweenQuery = `
		DELETE FROM whatsapp_history_sync_message
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3 AND timestamp<=$4 AND timestamp>=$5
	`
	deleteAllHistorySyncMessagesQuery       = "DELETE FROM whatsapp_history_sync_message WHERE bridge_id=$1 AND user_login_id=$2"
	deleteHistorySyncMessagesForPortalQuery = `
		DELETE FROM whatsapp_history_sync_message
		WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
	`
	conversationHasHistorySyncMessagesQuery = `
		SELECT EXISTS(
		    SELECT 1 FROM whatsapp_history_sync_message
			WHERE bridge_id=$1 AND user_login_id=$2 AND chat_jid=$3
		)
	`
)

type HistorySyncMessageTuple struct {
	Info    *types.MessageInfo
	Message []byte
}

func (t *HistorySyncMessageTuple) GetMassInsertValues() [4]any {
	return [4]any{t.Info.Sender.ToNonAD(), t.Info.ID, t.Info.Timestamp.Unix(), t.Message}
}

var batchInsertHistorySyncMessage = dbutil.NewMassInsertBuilder[*HistorySyncMessageTuple, [4]any](
	insertHistorySyncMessageQuery, "($1, $2, $3, $%d, $%d, $%d, $%d, $4)",
)

func (mq *MessageQuery) Put(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID, messages []*HistorySyncMessageTuple) error {
	return mq.DoTxn(ctx, nil, func(ctx context.Context) error {
		for _, chunk := range exslices.Chunk(messages, 50) {
			query, params := batchInsertHistorySyncMessage.Build([4]any{mq.BridgeID, loginID, chatJID, time.Now().Unix()}, chunk)
			_, err := mq.Exec(ctx, query, params...)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func scanWebMessageInfo(rows dbutil.Scannable) (*waWeb.WebMessageInfo, error) {
	var msgData []byte
	err := rows.Scan(&msgData)
	if err != nil {
		return nil, err
	}
	var historySyncMsg waHistorySync.HistorySyncMsg
	err = proto.Unmarshal(msgData, &historySyncMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return historySyncMsg.GetMessage(), nil
}

var webMessageInfoConverter = dbutil.ConvertRowFn[*waWeb.WebMessageInfo](scanWebMessageInfo)

func (mq *MessageQuery) GetBetween(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID, startTime, endTime *time.Time, limit int) ([]*waWeb.WebMessageInfo, error) {
	whereClauses := ""
	args := []any{mq.BridgeID, loginID, chatJID}
	argNum := 4
	if startTime != nil {
		whereClauses += fmt.Sprintf(" AND timestamp >= $%d", argNum)
		args = append(args, startTime.Unix())
		argNum++
	}
	if endTime != nil {
		whereClauses += fmt.Sprintf(" AND timestamp <= $%d", argNum)
		args = append(args, endTime.Unix())
	}

	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("LIMIT %d", limit)
	}
	query := fmt.Sprintf(getHistorySyncMessagesBetweenQueryTemplate, whereClauses, limitClause)

	return webMessageInfoConverter.
		NewRowIter(mq.Query(ctx, query, args...)).
		AsList()
}

func (mq *MessageQuery) DeleteBetween(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID, before, after uint64) error {
	_, err := mq.Exec(ctx, deleteHistorySyncMessagesBetweenQuery, mq.BridgeID, loginID, chatJID, before, after)
	return err
}

func (mq *MessageQuery) DeleteAll(ctx context.Context, loginID networkid.UserLoginID) error {
	_, err := mq.Exec(ctx, deleteAllHistorySyncMessagesQuery, mq.BridgeID, loginID)
	return err
}

func (mq *MessageQuery) DeleteAllInChat(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID) error {
	_, err := mq.Exec(ctx, deleteHistorySyncMessagesForPortalQuery, mq.BridgeID, loginID, chatJID)
	return err
}

func (mq *MessageQuery) ConversationHasMessages(ctx context.Context, loginID networkid.UserLoginID, chatJID types.JID) (exists bool, err error) {
	err = mq.QueryRow(ctx, conversationHasHistorySyncMessagesQuery, mq.BridgeID, loginID, chatJID).Scan(&exists)
	return
}
