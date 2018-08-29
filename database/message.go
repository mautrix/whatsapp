// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
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

	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/types"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

func (mq *MessageQuery) CreateTable() error {
	_, err := mq.db.Exec(`CREATE TABLE IF NOT EXISTS message (
		chat_jid      VARCHAR(25),
		chat_receiver VARCHAR(25),
		jid  VARCHAR(255),
		mxid VARCHAR(255) NOT NULL UNIQUE,

		PRIMARY KEY (chat_jid, chat_receiver, jid),
		FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver)
	)`)
	return err
}

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) GetAll(chat PortalKey) (messages []*Message) {
	rows, err := mq.db.Query("SELECT * FROM message WHERE chat_jid=? AND chat_receiver=?", chat.JID, chat.Receiver)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) GetByJID(chat PortalKey, jid types.WhatsAppMessageID) *Message {
	return mq.get("SELECT * FROM message WHERE chat_jid=? AND chat_receiver=? AND jid=?", chat.JID, chat.Receiver, jid)
}

func (mq *MessageQuery) GetByMXID(mxid types.MatrixEventID) *Message {
	return mq.get("SELECT * FROM message WHERE mxid=?", mxid)
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

	Chat PortalKey
	JID     types.WhatsAppMessageID
	MXID    types.MatrixEventID
}

func (msg *Message) Scan(row Scannable) *Message {
	err := row.Scan(&msg.Chat.JID, &msg.Chat.Receiver, &msg.JID, &msg.MXID)
	if err != nil {
		if err != sql.ErrNoRows {
			msg.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return msg
}

func (msg *Message) Insert() error {
	_, err := msg.db.Exec("INSERT INTO message VALUES (?, ?, ?, ?)", msg.Chat.JID, msg.Chat.Receiver, msg.JID, msg.MXID)
	if err != nil {
		msg.log.Warnfln("Failed to insert %s: %v", msg.Chat, msg.JID, err)
	}
	return err
}

func (msg *Message) Update() error {
	_, err := msg.db.Exec("UPDATE portal SET mxid=? WHERE chat_jid=? AND chat_receiver=? AND jid=?", msg.MXID, msg.Chat.JID, msg.Chat.Receiver, msg.JID)
	if err != nil {
		msg.log.Warnfln("Failed to update %s: %v", msg.Chat, msg.JID, err)
	}
	return err
}
