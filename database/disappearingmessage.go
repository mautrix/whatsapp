// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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
	"time"

	"go.mau.fi/util/dbutil"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
)

type DisappearingMessageQuery struct {
	db  *Database
	log log.Logger
}

func (dmq *DisappearingMessageQuery) New() *DisappearingMessage {
	return &DisappearingMessage{
		db:  dmq.db,
		log: dmq.log,
	}
}

func (dmq *DisappearingMessageQuery) NewWithValues(roomID id.RoomID, eventID id.EventID, expireIn time.Duration, expireAt time.Time) *DisappearingMessage {
	dm := &DisappearingMessage{
		db:       dmq.db,
		log:      dmq.log,
		RoomID:   roomID,
		EventID:  eventID,
		ExpireIn: expireIn,
		ExpireAt: expireAt,
	}
	return dm
}

const (
	getAllScheduledDisappearingMessagesQuery = `
		SELECT room_id, event_id, expire_in, expire_at FROM disappearing_message WHERE expire_at IS NOT NULL AND expire_at <= $1
	`
)

func (dmq *DisappearingMessageQuery) GetUpcomingScheduled(duration time.Duration) (messages []*DisappearingMessage) {
	rows, err := dmq.db.Query(getAllScheduledDisappearingMessagesQuery, time.Now().Add(duration).UnixMilli())
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		messages = append(messages, dmq.New().Scan(rows))
	}
	return
}

type DisappearingMessage struct {
	db  *Database
	log log.Logger

	RoomID   id.RoomID
	EventID  id.EventID
	ExpireIn time.Duration
	ExpireAt time.Time
}

func (msg *DisappearingMessage) Scan(row dbutil.Scannable) *DisappearingMessage {
	var expireIn int64
	var expireAt sql.NullInt64
	err := row.Scan(&msg.RoomID, &msg.EventID, &expireIn, &expireAt)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			msg.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	msg.ExpireIn = time.Duration(expireIn) * time.Millisecond
	if expireAt.Valid {
		msg.ExpireAt = time.UnixMilli(expireAt.Int64)
	}
	return msg
}

func (msg *DisappearingMessage) Insert(txn dbutil.Execable) {
	if txn == nil {
		txn = msg.db
	}
	var expireAt sql.NullInt64
	if !msg.ExpireAt.IsZero() {
		expireAt.Valid = true
		expireAt.Int64 = msg.ExpireAt.UnixMilli()
	}
	_, err := txn.Exec(`INSERT INTO disappearing_message (room_id, event_id, expire_in, expire_at) VALUES ($1, $2, $3, $4)`,
		msg.RoomID, msg.EventID, msg.ExpireIn.Milliseconds(), expireAt)
	if err != nil {
		msg.log.Warnfln("Failed to insert %s/%s: %v", msg.RoomID, msg.EventID, err)
	}
}

func (msg *DisappearingMessage) StartTimer() {
	msg.ExpireAt = time.Now().Add(msg.ExpireIn * time.Second)
	_, err := msg.db.Exec("UPDATE disappearing_message SET expire_at=$1 WHERE room_id=$2 AND event_id=$3", msg.ExpireAt.Unix(), msg.RoomID, msg.EventID)
	if err != nil {
		msg.log.Warnfln("Failed to update %s/%s: %v", msg.RoomID, msg.EventID, err)
	}
}

func (msg *DisappearingMessage) Delete() {
	_, err := msg.db.Exec("DELETE FROM disappearing_message WHERE room_id=$1 AND event_id=$2", msg.RoomID, msg.EventID)
	if err != nil {
		msg.log.Warnfln("Failed to delete %s/%s: %v", msg.RoomID, msg.EventID, err)
	}
}
