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
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/types"
	"database/sql"
)

type MessageQuery struct {
	db  *Database
	log log.Logger
}

func (mq *MessageQuery) CreateTable() error {
	_, err := mq.db.Exec(`CREATE TABLE IF NOT EXISTS message (
		owner VARCHAR(255),
		jid   VARCHAR(255),
		mxid  VARCHAR(255) NOT NULL UNIQUE,

		PRIMARY KEY (owner, jid),
		FOREIGN KEY (owner) REFERENCES user(mxid)
	)`)
	return err
}

func (mq *MessageQuery) New() *Message {
	return &Message{
		db:  mq.db,
		log: mq.log,
	}
}

func (mq *MessageQuery) GetAll(owner types.MatrixUserID) (messages []*Message) {
	rows, err := mq.db.Query("SELECT * FROM message WHERE owner=?", owner)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		messages = append(messages, mq.New().Scan(rows))
	}
	return
}

func (mq *MessageQuery) GetByJID(owner types.MatrixUserID, jid types.WhatsAppMessageID) *Message {
	return mq.get("SELECT * FROM message WHERE owner=? AND jid=?", owner, jid)
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

	Owner types.MatrixUserID
	JID   types.WhatsAppMessageID
	MXID  types.MatrixEventID
}

func (msg *Message) Scan(row Scannable) *Message {
	err := row.Scan(&msg.Owner, &msg.JID, &msg.MXID)
	if err != nil {
		if err != sql.ErrNoRows {
			msg.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return msg
}

func (msg *Message) Insert() error {
	_, err := msg.db.Exec("INSERT INTO message VALUES (?, ?, ?)", msg.Owner, msg.JID, msg.MXID)
	if err != nil {
		msg.log.Warnfln("Failed to update %s->%s: %v", msg.Owner, msg.JID, err)
	}
	return err
}

func (msg *Message) Update() error {
	_, err := msg.db.Exec("UPDATE portal SET mxid=? WHERE owner=? AND jid=?", msg.MXID, msg.Owner, msg.JID)
	if err != nil {
		msg.log.Warnfln("Failed to update %s->%s: %v", msg.Owner, msg.JID, err)
	}
	return err
}
