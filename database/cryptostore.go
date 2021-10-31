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

//go:build cgo && !nocrypto

package database

import (
	"database/sql"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"
)

type SQLCryptoStore struct {
	*crypto.SQLCryptoStore
	UserID        id.UserID
	GhostIDFormat string
}

var _ crypto.Store = (*SQLCryptoStore)(nil)

func NewSQLCryptoStore(db *Database, userID id.UserID, ghostIDFormat string) *SQLCryptoStore {
	return &SQLCryptoStore{
		SQLCryptoStore: crypto.NewSQLCryptoStore(db.DB, db.dialect, "", "",
			[]byte("maunium.net/go/mautrix-whatsapp"),
			&cryptoLogger{db.log.Sub("CryptoStore")}),
		UserID:        userID,
		GhostIDFormat: ghostIDFormat,
	}
}

func (store *SQLCryptoStore) FindDeviceID() (deviceID id.DeviceID) {
	err := store.DB.QueryRow("SELECT device_id FROM crypto_account WHERE account_id=$1", store.AccountID).Scan(&deviceID)
	if err != nil && err != sql.ErrNoRows {
		store.Log.Warn("Failed to scan device ID: %v", err)
	}
	return
}

func (store *SQLCryptoStore) GetRoomMembers(roomID id.RoomID) (members []id.UserID, err error) {
	var rows *sql.Rows
	rows, err = store.DB.Query(`
		SELECT user_id FROM mx_user_profile
		WHERE room_id=$1
			AND (membership='join' OR membership='invite')
			AND user_id<>$2
			AND user_id NOT LIKE $3
	`, roomID, store.UserID, store.GhostIDFormat)
	if err != nil {
		return
	}
	for rows.Next() {
		var userID id.UserID
		err := rows.Scan(&userID)
		if err != nil {
			store.Log.Warn("Failed to scan member in %s: %v", roomID, err)
		} else {
			members = append(members, userID)
		}
	}
	return
}

// TODO merge this with the one in the parent package
type cryptoLogger struct {
	int log.Logger
}

var levelTrace = log.Level{
	Name:     "Trace",
	Severity: -10,
	Color:    -1,
}

func (c *cryptoLogger) Error(message string, args ...interface{}) {
	c.int.Errorfln(message, args...)
}

func (c *cryptoLogger) Warn(message string, args ...interface{}) {
	c.int.Warnfln(message, args...)
}

func (c *cryptoLogger) Debug(message string, args ...interface{}) {
	c.int.Debugfln(message, args...)
}

func (c *cryptoLogger) Trace(message string, args ...interface{}) {
	c.int.Logfln(levelTrace, message, args...)
}
