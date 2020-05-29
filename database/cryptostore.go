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

// +build cgo

package database

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/id"
)

type SQLCryptoStore struct {
	db  *Database
	log log.Logger

	UserID    id.UserID
	DeviceID  id.DeviceID
	SyncToken string
	PickleKey []byte
	Account   *crypto.OlmAccount

	GhostIDFormat string
}

var _ crypto.Store = (*SQLCryptoStore)(nil)

func NewSQLCryptoStore(db *Database, deviceID id.DeviceID) *SQLCryptoStore {
	return &SQLCryptoStore{
		db:        db,
		log:       db.log.Sub("CryptoStore"),
		PickleKey: []byte("maunium.net/go/mautrix-whatsapp"),
		DeviceID:  deviceID,
	}
}

func (db *Database) FindDeviceID() (deviceID id.DeviceID) {
	err := db.QueryRow("SELECT device_id FROM crypto_account LIMIT 1").Scan(&deviceID)
	if err != nil && err != sql.ErrNoRows {
		db.log.Warnln("Failed to scan device ID:", err)
	}
	return
}

func (store *SQLCryptoStore) GetRoomMembers(roomID id.RoomID) (members []id.UserID, err error) {
	var rows *sql.Rows
	rows, err = store.db.Query(`
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
			store.log.Warnfln("Failed to scan member in %s: %v", roomID, err)
		} else {
			members = append(members, userID)
		}
	}
	return
}

func (store *SQLCryptoStore) Flush() error {
	return nil
}

func (store *SQLCryptoStore) PutNextBatch(nextBatch string) {
	store.SyncToken = nextBatch
	_, err := store.db.Exec(`UPDATE crypto_account SET sync_token=$1 WHERE device_id=$2`, store.SyncToken, store.DeviceID)
	if err != nil {
		store.log.Warnln("Failed to store sync token:", err)
	}
}

func (store *SQLCryptoStore) GetNextBatch() string {
	if store.SyncToken == "" {
		err := store.db.
			QueryRow("SELECT sync_token FROM crypto_account WHERE device_id=$1", store.DeviceID).
			Scan(&store.SyncToken)
		if err != nil && err != sql.ErrNoRows {
			store.log.Warnln("Failed to scan sync token:", err)
		}
	}
	return store.SyncToken
}

func (store *SQLCryptoStore) PutAccount(account *crypto.OlmAccount) error {
	store.Account = account
	bytes := account.Internal.Pickle(store.PickleKey)
	var err error
	if store.db.dialect == "postgres" {
		_, err = store.db.Exec(`
			INSERT INTO crypto_account (device_id, shared, sync_token, account) VALUES ($1, $2, $3, $4)
			ON CONFLICT (device_id) DO UPDATE SET shared=$2, sync_token=$3, account=$4`,
			store.DeviceID, account.Shared, store.SyncToken, bytes)
	} else if store.db.dialect == "sqlite3" {
		_, err = store.db.Exec("INSERT OR REPLACE INTO crypto_account (device_id, shared, sync_token, account) VALUES ($1, $2, $3, $4)",
			store.DeviceID, account.Shared, store.SyncToken, bytes)
	} else {
		err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	}
	if err != nil {
		store.log.Warnln("Failed to store account:", err)
	}
	return nil
}

func (store *SQLCryptoStore) GetAccount() (*crypto.OlmAccount, error) {
	if store.Account == nil {
		row := store.db.QueryRow("SELECT shared, sync_token, account FROM crypto_account WHERE device_id=$1", store.DeviceID)
		acc := &crypto.OlmAccount{Internal: *olm.NewBlankAccount()}
		var accountBytes []byte
		err := row.Scan(&acc.Shared, &store.SyncToken, &accountBytes)
		if err == sql.ErrNoRows {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		err = acc.Internal.Unpickle(accountBytes, store.PickleKey)
		if err != nil {
			return nil, err
		}
		store.Account = acc
	}
	return store.Account, nil
}

func (store *SQLCryptoStore) HasSession(key id.SenderKey) bool {
	// TODO this may need to be changed if olm sessions start expiring
	var sessionID id.SessionID
	err := store.db.QueryRow("SELECT session_id FROM crypto_olm_session WHERE sender_key=$1 LIMIT 1", key).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return false
	}
	return len(sessionID) > 0
}

func (store *SQLCryptoStore) GetSessions(key id.SenderKey) (crypto.OlmSessionList, error) {
	rows, err := store.db.Query("SELECT session, created_at, last_used FROM crypto_olm_session WHERE sender_key=$1 ORDER BY session_id", key)
	if err != nil {
		return nil, err
	}
	list := crypto.OlmSessionList{}
	for rows.Next() {
		sess := crypto.OlmSession{Internal: *olm.NewBlankSession()}
		var sessionBytes []byte
		err := rows.Scan(&sessionBytes, &sess.CreationTime, &sess.UseTime)
		if err != nil {
			return nil, err
		}
		err = sess.Internal.Unpickle(sessionBytes, store.PickleKey)
		if err != nil {
			return nil, err
		}
		list = append(list, &sess)
	}
	return list, nil
}

func (store *SQLCryptoStore) GetLatestSession(key id.SenderKey) (*crypto.OlmSession, error) {
	row := store.db.QueryRow("SELECT session, created_at, last_used FROM crypto_olm_session WHERE sender_key=$1 ORDER BY session_id DESC LIMIT 1", key)
	sess := crypto.OlmSession{Internal: *olm.NewBlankSession()}
	var sessionBytes []byte
	err := row.Scan(&sessionBytes, &sess.CreationTime, &sess.UseTime)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &sess, sess.Internal.Unpickle(sessionBytes, store.PickleKey)
}

func (store *SQLCryptoStore) AddSession(key id.SenderKey, session *crypto.OlmSession) error {
	sessionBytes := session.Internal.Pickle(store.PickleKey)
	_, err := store.db.Exec("INSERT INTO crypto_olm_session (session_id, sender_key, session, created_at, last_used) VALUES ($1, $2, $3, $4, $5)",
		session.ID(), key, sessionBytes, session.CreationTime, session.UseTime)
	return err
}

func (store *SQLCryptoStore) UpdateSession(key id.SenderKey, session *crypto.OlmSession) error {
	sessionBytes := session.Internal.Pickle(store.PickleKey)
	_, err := store.db.Exec("UPDATE crypto_olm_session SET session=$1, last_used=$2 WHERE session_id=$3",
		sessionBytes, session.UseTime, session.ID())
	return err
}

func (store *SQLCryptoStore) PutGroupSession(roomID id.RoomID, senderKey id.SenderKey, sessionID id.SessionID, session *crypto.InboundGroupSession) error {
	sessionBytes := session.Internal.Pickle(store.PickleKey)
	forwardingChains := strings.Join(session.ForwardingChains, ",")
	_, err := store.db.Exec("INSERT INTO crypto_megolm_inbound_session (session_id, sender_key, signing_key, room_id, session, forwarding_chains) VALUES ($1, $2, $3, $4, $5, $6)",
		sessionID, senderKey, session.SigningKey, roomID, sessionBytes, forwardingChains)
	return err
}

func (store *SQLCryptoStore) GetGroupSession(roomID id.RoomID, senderKey id.SenderKey, sessionID id.SessionID) (*crypto.InboundGroupSession, error) {
	var signingKey id.Ed25519
	var sessionBytes []byte
	var forwardingChains string
	err := store.db.QueryRow(`
		SELECT signing_key, session, forwarding_chains
		FROM crypto_megolm_inbound_session
		WHERE room_id=$1 AND sender_key=$2 AND session_id=$3`,
		roomID, senderKey, sessionID,
	).Scan(&signingKey, &sessionBytes, &forwardingChains)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	igs := olm.NewBlankInboundGroupSession()
	err = igs.Unpickle(sessionBytes, store.PickleKey)
	if err != nil {
		return nil, err
	}
	return &crypto.InboundGroupSession{
		Internal:         *igs,
		SigningKey:       signingKey,
		SenderKey:        senderKey,
		RoomID:           roomID,
		ForwardingChains: strings.Split(forwardingChains, ","),
	}, nil
}

func (store *SQLCryptoStore) AddOutboundGroupSession(session *crypto.OutboundGroupSession) (err error) {
	sessionBytes := session.Internal.Pickle(store.PickleKey)
	if store.db.dialect == "postgres" {
		_, err = store.db.Exec(`
			INSERT INTO crypto_megolm_outbound_session (
				room_id, session_id, session, shared, max_messages, message_count, max_age, created_at, last_used
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (room_id) DO UPDATE SET session_id=$2, session=$3, shared=$4, max_messages=$5, message_count=$6, max_age=$7, created_at=$8, last_used=$9`,
			session.RoomID, session.ID(), sessionBytes, session.Shared, session.MaxMessages, session.MessageCount, session.MaxAge, session.CreationTime, session.UseTime)
	} else if store.db.dialect == "sqlite3" {
		_, err = store.db.Exec(`
			INSERT OR REPLACE INTO crypto_megolm_outbound_session (
				room_id, session_id, session, shared, max_messages, message_count, max_age, created_at, last_used
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			session.RoomID, session.ID(), sessionBytes, session.Shared, session.MaxMessages, session.MessageCount, session.MaxAge, session.CreationTime, session.UseTime)
	}  else {
		err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	}
	return
}

func (store *SQLCryptoStore) UpdateOutboundGroupSession(session *crypto.OutboundGroupSession) error {
	sessionBytes := session.Internal.Pickle(store.PickleKey)
	_, err := store.db.Exec("UPDATE crypto_megolm_outbound_session SET session=$1, message_count=$2, last_used=$3 WHERE room_id=$4 AND session_id=$5",
		sessionBytes, session.MessageCount, session.UseTime, session.RoomID, session.ID())
	return err
}

func (store *SQLCryptoStore) GetOutboundGroupSession(roomID id.RoomID) (*crypto.OutboundGroupSession, error) {
	var ogs crypto.OutboundGroupSession
	var sessionBytes []byte
	err := store.db.QueryRow(`
		SELECT session, shared, max_messages, message_count, max_age, created_at, last_used
		FROM crypto_megolm_outbound_session WHERE room_id=$1`,
		roomID,
	).Scan(&sessionBytes, &ogs.Shared, &ogs.MaxMessages, &ogs.MessageCount, &ogs.MaxAge, &ogs.CreationTime, &ogs.UseTime)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	intOGS := olm.NewBlankOutboundGroupSession()
	err = intOGS.Unpickle(sessionBytes, store.PickleKey)
	if err != nil {
		return nil, err
	}
	ogs.Internal = *intOGS
	ogs.RoomID = roomID
	return &ogs, nil
}

func (store *SQLCryptoStore) RemoveOutboundGroupSession(roomID id.RoomID) error {
	_, err := store.db.Exec("DELETE FROM crypto_megolm_outbound_session WHERE room_id=$1", roomID)
	return err
}

func (store *SQLCryptoStore) ValidateMessageIndex(senderKey id.SenderKey, sessionID id.SessionID, eventID id.EventID, index uint, timestamp int64) bool {
	var resultEventID id.EventID
	var resultTimestamp int64
	err := store.db.QueryRow(
		`SELECT event_id, timestamp FROM crypto_message_index WHERE sender_key=$1 AND session_id=$2 AND "index"=$3`,
		senderKey, sessionID, index,
	).Scan(&resultEventID, &resultTimestamp)
	if err == sql.ErrNoRows {
		_, err := store.db.Exec(`INSERT INTO crypto_message_index (sender_key, session_id, "index", event_id, timestamp) VALUES ($1, $2, $3, $4, $5)`,
			senderKey, sessionID, index, eventID, timestamp)
		if err != nil {
			store.log.Warnln("Failed to store message index:", err)
		}
		return true
	} else if err != nil {
		store.log.Warnln("Failed to scan message index:", err)
		return true
	}
	if resultEventID != eventID || resultTimestamp != timestamp {
		return false
	}
	return true
}

func (store *SQLCryptoStore) GetDevices(userID id.UserID) (map[id.DeviceID]*crypto.DeviceIdentity, error) {
	var ignore id.UserID
	err := store.db.QueryRow("SELECT user_id FROM crypto_tracked_user WHERE user_id=$1", userID).Scan(&ignore)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	rows, err := store.db.Query("SELECT device_id, identity_key, signing_key, trust, deleted, name FROM crypto_device WHERE user_id=$1", userID)
	if err != nil {
		return nil, err
	}
	data := make(map[id.DeviceID]*crypto.DeviceIdentity)
	for rows.Next() {
		var identity crypto.DeviceIdentity
		err := rows.Scan(&identity.DeviceID, &identity.IdentityKey, &identity.SigningKey, &identity.Trust, &identity.Deleted, &identity.Name)
		if err != nil {
			return nil, err
		}
		identity.UserID = userID
		data[identity.DeviceID] = &identity
	}
	return data, nil
}

func (store *SQLCryptoStore) GetDevice(userID id.UserID, deviceID id.DeviceID) (*crypto.DeviceIdentity, error) {
	var identity crypto.DeviceIdentity
	err := store.db.QueryRow(`
		SELECT identity_key, signing_key, trust, deleted, name
		FROM crypto_device WHERE user_id=$1 AND device_id=$2`,
		userID, deviceID,
	).Scan(&identity.IdentityKey, &identity.SigningKey, &identity.Trust, &identity.Deleted, &identity.Name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &identity, nil
}

func (store *SQLCryptoStore) PutDevices(userID id.UserID, devices map[id.DeviceID]*crypto.DeviceIdentity) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}

	if store.db.dialect == "postgres" {
		_, err = tx.Exec("INSERT INTO crypto_tracked_user (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING", userID)
	} else if store.db.dialect == "sqlite3" {
		_, err = tx.Exec("INSERT OR IGNORE INTO crypto_tracked_user (user_id) VALUES ($1)", userID)
	} else {
		err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	}
	if err != nil {
		return errors.Wrap(err, "failed to add user to tracked users list")
	}

	_, err = tx.Exec("DELETE FROM crypto_device WHERE user_id=$1", userID)
	if err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "failed to delete old devices")
	}
	if len(devices) == 0 {
		err = tx.Commit()
		if err != nil {
			return errors.Wrap(err, "failed to commit changes (no devices added)")
		}
		return nil
	}
	// TODO do this in batches to avoid too large db queries
	values := make([]interface{}, 1, len(devices)*6+1)
	values[0] = userID
	valueStrings := make([]string, 0, len(devices))
	i := 2
	for deviceID, identity := range devices {
		values = append(values, deviceID, identity.IdentityKey, identity.SigningKey, identity.Trust, identity.Deleted, identity.Name)
		valueStrings = append(valueStrings, fmt.Sprintf("($1, $%d, $%d, $%d, $%d, $%d, $%d)", i, i+1, i+2, i+3, i+4, i+5))
		i += 6
	}
	valueString := strings.Join(valueStrings, ",")
	_, err = tx.Exec("INSERT INTO crypto_device (user_id, device_id, identity_key, signing_key, trust, deleted, name) VALUES "+valueString, values...)
	if err != nil {
		_ = tx.Rollback()
		return errors.Wrap(err, "failed to insert new devices")
	}
	err = tx.Commit()
	if err != nil {
		return errors.Wrap(err, "failed to commit changes")
	}
	return nil
}

func (store *SQLCryptoStore) FilterTrackedUsers(users []id.UserID) []id.UserID {
	var rows *sql.Rows
	var err error
	if store.db.dialect == "postgres" {
		rows, err = store.db.Query("SELECT user_id FROM crypto_tracked_user WHERE user_id = ANY($1)", pq.Array(users))
	} else {
		queryString := make([]string, len(users))
		params := make([]interface{}, len(users))
		for i, user := range users {
			queryString[i] = fmt.Sprintf("$%d", i+1)
			params[i] = user
		}
		rows, err = store.db.Query("SELECT user_id FROM crypto_tracked_user WHERE user_id IN ("+strings.Join(queryString, ",")+")", params...)
	}
	if err != nil {
		store.log.Warnln("Failed to filter tracked users:", err)
		return users
	}
	var ptr int
	for rows.Next() {
		err = rows.Scan(&users[ptr])
		if err != nil {
			store.log.Warnln("Failed to tracked user ID:", err)
		} else {
			ptr++
		}
	}
	return users[:ptr]
}
