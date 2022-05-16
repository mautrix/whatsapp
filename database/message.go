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

package database

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"

	"go.mau.fi/whatsmeow/types"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

const (
	getAllMessagesQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2
	`
	getMessageByJIDQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND jid=$3
	`
	getMessageByMXIDQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE mxid=$1
	`
	getLastMessageInChatQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND timestamp<=$3 AND sent=true ORDER BY timestamp DESC LIMIT 1
	`
	getFirstMessageInChatQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND sent=true ORDER BY timestamp ASC LIMIT 1
	`
	getMessagesBetweenQuery = `
		SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid FROM message
		WHERE chat_jid=$1 AND chat_receiver=$2 AND timestamp>$3 AND timestamp<=$4 AND sent=true ORDER BY timestamp ASC
	`
)

func (mq *MessageQuery) GetAll(chat PortalKey) (messages []*Message) {
	rows, err := mq.db.Query(getAllMessagesQuery, chat.JID, chat.Receiver)
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) GetByJID(chat PortalKey, jid types.MessageID) *Message {
	return mq.maybeScan(mq.db.QueryRow(getMessageByJIDQuery, chat.JID, chat.Receiver, jid))
}

func (mq *MessageQuery) GetByMXID(mxid id.EventID) *Message {
	return mq.maybeScan(mq.db.QueryRow(getMessageByMXIDQuery, mxid))
}

func (mq *MessageQuery) GetLastInChat(chat PortalKey) *Message {
	return mq.GetLastInChatBefore(chat, time.Now().Add(60*time.Second))
}

func (mq *MessageQuery) GetLastInChatBefore(chat PortalKey, maxTimestamp time.Time) *Message {
	msg := mq.maybeScan(mq.db.QueryRow(getLastMessageInChatQuery, chat.JID, chat.Receiver, maxTimestamp.Unix()))
	if msg == nil || msg.Timestamp.IsZero() {
		// Old db, we don't know what the last message is.
		return nil
	}
	return msg
}

func (mq *MessageQuery) GetFirstInChat(chat PortalKey) *Message {
	return mq.maybeScan(mq.db.QueryRow(getFirstMessageInChatQuery, chat.JID, chat.Receiver))
}

func (mq *MessageQuery) GetMessagesBetween(chat PortalKey, minTimestamp, maxTimestamp time.Time) (messages []*Message) {
	rows, err := mq.db.Query(getMessagesBetweenQuery, chat.JID, chat.Receiver, minTimestamp.Unix(), maxTimestamp.Unix())
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) maybeScan(row *sql.Row) *Message {
	if row == nil {
		return nil
	}
	return mq.New().Scan(row)
}

type MessageErrorType string

const (
	MsgNoError             MessageErrorType = ""
	MsgErrDecryptionFailed MessageErrorType = "decryption_failed"
	MsgErrMediaNotFound    MessageErrorType = "media_not_found"
)

type MessageType string

const (
	MsgUnknown  MessageType = ""
	MsgFake     MessageType = "fake"
	MsgNormal   MessageType = "message"
	MsgReaction MessageType = "reaction"
)

type Message struct {
	db  *Database
	log log.Logger

	Chat      PortalKey
	JID       types.MessageID
	MXID      id.EventID
	Sender    types.JID
	Timestamp time.Time
	Sent      bool
	Type      MessageType
	Error     MessageErrorType

	BroadcastListJID types.JID
}

func (msg *Message) IsFakeMXID() bool {
	return strings.HasPrefix(msg.MXID.String(), "net.maunium.whatsapp.fake::")
}

func (msg *Message) IsFakeJID() bool {
	return strings.HasPrefix(msg.JID, "FAKE::") || msg.JID == string(msg.MXID)
}

func (msg *Message) Scan(row Scannable) *Message {
	var ts int64
	err := row.Scan(&msg.Chat.JID, &msg.Chat.Receiver, &msg.JID, &msg.MXID, &msg.Sender, &ts, &msg.Sent, &msg.Type, &msg.Error, &msg.BroadcastListJID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			msg.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	if ts != 0 {
		msg.Timestamp = time.Unix(ts, 0)
	}
	return msg
}

func (msg *Message) Insert(txn *sql.Tx) {
	var sender interface{} = msg.Sender
	// Slightly hacky hack to allow inserting empty senders (used for post-backfill dummy events)
	if msg.Sender.IsEmpty() {
		sender = ""
	}
	query := `
		INSERT INTO message
			(chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent, type, error, broadcast_list_jid)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	args := []interface{}{
		msg.Chat.JID, msg.Chat.Receiver, msg.JID, msg.MXID, sender, msg.Timestamp.Unix(), msg.Sent, msg.Type, msg.Error, msg.BroadcastListJID,
	}
	var err error
	if txn != nil {
		_, err = txn.Exec(query, args...)
	} else {
		_, err = msg.db.Exec(query, args...)
	}
	if err != nil {
		msg.log.Warnfln("Failed to insert %s@%s: %v", msg.Chat, msg.JID, err)
	}
}

func (msg *Message) MarkSent(ts time.Time) {
	msg.Sent = true
	msg.Timestamp = ts
	_, err := msg.db.Exec("UPDATE message SET sent=true, timestamp=$1 WHERE chat_jid=$2 AND chat_receiver=$3 AND jid=$4", ts.Unix(), msg.Chat.JID, msg.Chat.Receiver, msg.JID)
	if err != nil {
		msg.log.Warnfln("Failed to update %s@%s: %v", msg.Chat, msg.JID, err)
	}
}

func (msg *Message) UpdateMXID(txn *sql.Tx, mxid id.EventID, newType MessageType, newError MessageErrorType) {
	msg.MXID = mxid
	msg.Type = newType
	msg.Error = newError
	query := "UPDATE message SET mxid=$1, type=$2, error=$3 WHERE chat_jid=$4 AND chat_receiver=$5 AND jid=$6"
	args := []interface{}{mxid, newType, newError, msg.Chat.JID, msg.Chat.Receiver, msg.JID}
	var err error
	if txn != nil {
		_, err = txn.Exec(query, args...)
	} else {
		_, err = msg.db.Exec(query, args...)
	}
	if err != nil {
		msg.log.Warnfln("Failed to update %s@%s: %v", msg.Chat, msg.JID, err)
	}
}

func (msg *Message) Delete() {
	_, err := msg.db.Exec("DELETE FROM message WHERE chat_jid=$1 AND chat_receiver=$2 AND jid=$3", msg.Chat.JID, msg.Chat.Receiver, msg.JID)
	if err != nil {
		msg.log.Warnfln("Failed to delete %s@%s: %v", msg.Chat, msg.JID, err)
	}
}
