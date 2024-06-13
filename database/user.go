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
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/dbutil"
)

type UserQuery struct {
	*dbutil.QueryHelper[*User]
}

func newUser(qh *dbutil.QueryHelper[*User]) *User {
	return &User{
		qh: qh,

		lastReadCache: make(map[PortalKey]time.Time),
		inSpaceCache:  make(map[PortalKey]bool),
	}
}

const (
	getAllUsersQuery       = `SELECT mxid, username, agent, device, management_room, space_room, phone_last_seen, phone_last_pinged, timezone FROM "user"`
	getUserByMXIDQuery     = getAllUsersQuery + ` WHERE mxid=$1`
	getUserByUsernameQuery = getAllUsersQuery + ` WHERE username=$1`
	insertUserQuery        = `
		INSERT INTO "user" (
			mxid, username, agent, device,
			management_room, space_room,
			phone_last_seen, phone_last_pinged, timezone
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	updateUserQuery = `
		UPDATE "user"
		SET username=$2, agent=$3, device=$4,
		    management_room=$5, space_room=$6,
		    phone_last_seen=$7, phone_last_pinged=$8, timezone=$9
		WHERE mxid=$1
	`
	getUserLastAppStateKeyIDQuery = "SELECT key_id FROM whatsmeow_app_state_sync_keys WHERE jid=$1 ORDER BY timestamp DESC LIMIT 1"
)

func (uq *UserQuery) GetAll(ctx context.Context) ([]*User, error) {
	return uq.QueryMany(ctx, getAllUsersQuery)
}

func (uq *UserQuery) GetByMXID(ctx context.Context, userID id.UserID) (*User, error) {
	return uq.QueryOne(ctx, getUserByMXIDQuery, userID)
}

func (uq *UserQuery) GetByUsername(ctx context.Context, username string) (*User, error) {
	return uq.QueryOne(ctx, getUserByUsernameQuery, username)
}

type User struct {
	qh *dbutil.QueryHelper[*User]

	MXID            id.UserID
	JID             types.JID
	ManagementRoom  id.RoomID
	SpaceRoom       id.RoomID
	PhoneLastSeen   time.Time
	PhoneLastPinged time.Time
	Timezone        string

	lastReadCache     map[PortalKey]time.Time
	lastReadCacheLock sync.Mutex
	inSpaceCache      map[PortalKey]bool
	inSpaceCacheLock  sync.Mutex
}

func (user *User) Scan(row dbutil.Scannable) (*User, error) {
	var username, timezone sql.NullString
	var device, agent sql.NullInt16
	var phoneLastSeen, phoneLastPinged sql.NullInt64
	err := row.Scan(&user.MXID, &username, &agent, &device, &user.ManagementRoom, &user.SpaceRoom, &phoneLastSeen, &phoneLastPinged, &timezone)
	if err != nil {
		return nil, err
	}
	user.Timezone = timezone.String
	if len(username.String) > 0 {
		user.JID = types.JID{
			User:   username.String,
			Device: uint16(device.Int16),
			Server: types.DefaultUserServer,
		}
	}
	if phoneLastSeen.Valid {
		user.PhoneLastSeen = time.Unix(phoneLastSeen.Int64, 0)
	}
	if phoneLastPinged.Valid {
		user.PhoneLastPinged = time.Unix(phoneLastPinged.Int64, 0)
	}
	return user, nil
}

func (user *User) sqlVariables() []any {
	var username *string
	var agent, device *uint16
	if !user.JID.IsEmpty() {
		username = dbutil.StrPtr(user.JID.User)
		var zero uint16
		agent = &zero
		device = dbutil.NumPtr(user.JID.Device)
	}
	return []any{
		user.MXID, username, agent, device, user.ManagementRoom, user.SpaceRoom,
		dbutil.UnixPtr(user.PhoneLastSeen), dbutil.UnixPtr(user.PhoneLastPinged),
		user.Timezone,
	}
}

func (user *User) Insert(ctx context.Context) error {
	return user.qh.Exec(ctx, insertUserQuery, user.sqlVariables()...)
}

func (user *User) Update(ctx context.Context) error {
	return user.qh.Exec(ctx, updateUserQuery, user.sqlVariables()...)
}

func (user *User) GetLastAppStateKeyID(ctx context.Context) (keyID []byte, err error) {
	err = user.qh.GetDB().QueryRow(ctx, getUserLastAppStateKeyIDQuery, user.JID).Scan(&keyID)
	return
}
