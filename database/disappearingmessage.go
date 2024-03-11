// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"database/sql"
	"time"

	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/dbutil"
)

type DisappearingMessageQuery struct {
	*dbutil.QueryHelper[*DisappearingMessage]
}

func newDisappearingMessage(qh *dbutil.QueryHelper[*DisappearingMessage]) *DisappearingMessage {
	return &DisappearingMessage{
		qh: qh,
	}
}

func (dmq *DisappearingMessageQuery) NewWithValues(roomID id.RoomID, eventID id.EventID, expireIn time.Duration, expireAt time.Time) *DisappearingMessage {
	dm := &DisappearingMessage{
		qh: dmq.QueryHelper,

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
	insertDisappearingMessageQuery       = `INSERT INTO disappearing_message (room_id, event_id, expire_in, expire_at) VALUES ($1, $2, $3, $4)`
	updateDisappearingMessageExpiryQuery = "UPDATE disappearing_message SET expire_at=$1 WHERE room_id=$2 AND event_id=$3"
	deleteDisappearingMessageQuery       = "DELETE FROM disappearing_message WHERE room_id=$1 AND event_id=$2"
)

func (dmq *DisappearingMessageQuery) GetUpcomingScheduled(ctx context.Context, duration time.Duration) ([]*DisappearingMessage, error) {
	return dmq.QueryMany(ctx, getAllScheduledDisappearingMessagesQuery, time.Now().Add(duration).UnixMilli())
}

type DisappearingMessage struct {
	qh *dbutil.QueryHelper[*DisappearingMessage]

	RoomID   id.RoomID
	EventID  id.EventID
	ExpireIn time.Duration
	ExpireAt time.Time
}

func (msg *DisappearingMessage) Scan(row dbutil.Scannable) (*DisappearingMessage, error) {
	var expireIn int64
	var expireAt sql.NullInt64
	err := row.Scan(&msg.RoomID, &msg.EventID, &expireIn, &expireAt)
	if err != nil {
		return nil, err
	}
	msg.ExpireIn = time.Duration(expireIn) * time.Millisecond
	if expireAt.Valid {
		msg.ExpireAt = time.UnixMilli(expireAt.Int64)
	}
	return msg, nil
}

func (msg *DisappearingMessage) sqlVariables() []any {
	return []any{msg.RoomID, msg.EventID, msg.ExpireIn.Milliseconds(), dbutil.UnixMilliPtr(msg.ExpireAt)}
}

func (msg *DisappearingMessage) Insert(ctx context.Context) error {
	return msg.qh.Exec(ctx, insertDisappearingMessageQuery, msg.sqlVariables()...)
}

func (msg *DisappearingMessage) StartTimer(ctx context.Context) error {
	msg.ExpireAt = time.Now().Add(msg.ExpireIn * time.Second)
	return msg.qh.Exec(ctx, updateDisappearingMessageExpiryQuery, msg.ExpireAt.Unix(), msg.RoomID, msg.EventID)
}

func (msg *DisappearingMessage) Delete(ctx context.Context) error {
	return msg.qh.Exec(ctx, deleteDisappearingMessageQuery, msg.RoomID, msg.EventID)
}
