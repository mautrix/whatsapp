// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"strings"
	"time"

	"github.com/Rhymen/go-whatsapp"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
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

func (mq *MessageQuery) GetAll(chat PortalKey) (messages []*Message) {
	rows, err := mq.db.Query("SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent FROM message WHERE chat_jid=$1 AND chat_receiver=$2", chat.JID, chat.Receiver)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) GetByJID(chat PortalKey, jid whatsapp.MessageID) *Message {
	return mq.get("SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent "+
		"FROM message WHERE chat_jid=$1 AND chat_receiver=$2 AND jid=$3", chat.JID, chat.Receiver, jid)
}

func (mq *MessageQuery) GetByMXID(mxid id.EventID) *Message {
	return mq.get("SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent "+
		"FROM message WHERE mxid=$1", mxid)
}

func (mq *MessageQuery) GetLastInChat(chat PortalKey) *Message {
	return mq.GetLastInChatBefore(chat, time.Now().Unix()+60)
}

func (mq *MessageQuery) GetLastInChatBefore(chat PortalKey, maxTimestamp int64) *Message {
	msg := mq.get("SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent "+
		"FROM message WHERE chat_jid=$1 AND chat_receiver=$2 AND timestamp<=$3 AND sent=true ORDER BY timestamp DESC LIMIT 1",
		chat.JID, chat.Receiver, maxTimestamp)
	if msg == nil || msg.Timestamp == 0 {
		// Old db, we don't know what the last message is.
		return nil
	}
	return msg
}

func (mq *MessageQuery) get(query string, args ...interface{}) *Message {
	row := mq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}
	return mq.New().Scan(row)
}

type Message struct {
	db  *Database
	log log.Logger

	Chat      PortalKey
	JID       whatsapp.MessageID
	MXID      id.EventID
	Sender    whatsapp.JID
	Timestamp int64
	Sent      bool
}

func (msg *Message) IsFakeMXID() bool {
	return strings.HasPrefix(msg.MXID.String(), "net.maunium.whatsapp.fake::")
}

func (msg *Message) Scan(row Scannable) *Message {
	err := row.Scan(&msg.Chat.JID, &msg.Chat.Receiver, &msg.JID, &msg.MXID, &msg.Sender, &msg.Timestamp, &msg.Sent)
	if err != nil {
		if err != sql.ErrNoRows {
			msg.log.Errorln("Database scan failed:", err)
		}
		return nil
	}

	return msg
}

func (msg *Message) Insert() {
	_, err := msg.db.Exec(`INSERT INTO message
			(chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		msg.Chat.JID, msg.Chat.Receiver, msg.JID, msg.MXID, msg.Sender, msg.Timestamp, msg.Sent)
	if err != nil {
		msg.log.Warnfln("Failed to insert %s@%s: %v", msg.Chat, msg.JID, err)
	}
}

func (msg *Message) MarkSent() {
	msg.Sent = true
	_, err := msg.db.Exec("UPDATE message SET sent=true WHERE chat_jid=$1 AND chat_receiver=$2 AND jid=$3", msg.Chat.JID, msg.Chat.Receiver, msg.JID)
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
