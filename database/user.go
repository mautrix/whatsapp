// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2019 Tulir Asokan
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
	"fmt"
	"strings"
	"time"

	"github.com/Rhymen/go-whatsapp"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) New() *User {
	return &User{
		db:  uq.db,
		log: uq.log,
	}
}

func (uq *UserQuery) GetAll() (users []*User) {
	rows, err := uq.db.Query(`SELECT * FROM "user"`)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		users = append(users, uq.New().Scan(rows))
	}
	return
}

func (uq *UserQuery) GetByMXID(userID types.MatrixUserID) *User {
	row := uq.db.QueryRow(`SELECT * FROM "user" WHERE mxid=$1`, userID)
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

func (uq *UserQuery) GetByJID(userID types.WhatsAppID) *User {
	row := uq.db.QueryRow(`SELECT * FROM "user" WHERE jid=$1`, stripSuffix(userID))
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

type User struct {
	db  *Database
	log log.Logger

	MXID           types.MatrixUserID
	JID            types.WhatsAppID
	ManagementRoom types.MatrixRoomID
	Session        *whatsapp.Session
	LastConnection uint64
}

func (user *User) Scan(row Scannable) *User {
	var jid, clientID, clientToken, serverToken sql.NullString
	var encKey, macKey []byte
	err := row.Scan(&user.MXID, &jid, &user.ManagementRoom, &clientID, &clientToken, &serverToken, &encKey, &macKey,
		&user.LastConnection)
	if err != nil {
		if err != sql.ErrNoRows {
			user.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	if len(jid.String) > 0 && len(clientID.String) > 0 {
		user.JID = jid.String + whatsappExt.NewUserSuffix
		user.Session = &whatsapp.Session{
			ClientId:    clientID.String,
			ClientToken: clientToken.String,
			ServerToken: serverToken.String,
			EncKey:      encKey,
			MacKey:      macKey,
			Wid:         jid.String + whatsappExt.OldUserSuffix,
		}
	} else {
		user.Session = nil
	}
	return user
}

func stripSuffix(jid types.WhatsAppID) string {
	if len(jid) == 0 {
		return jid
	}

	index := strings.IndexRune(jid, '@')
	if index < 0 {
		return jid
	}

	return jid[:index]
}

func (user *User) jidPtr() *string {
	if len(user.JID) > 0 {
		str := stripSuffix(user.JID)
		return &str
	}
	return nil
}

func (user *User) sessionUnptr() (sess whatsapp.Session) {
	if user.Session != nil {
		sess = *user.Session
	}
	return
}

func (user *User) Insert() {
	sess := user.sessionUnptr()
	_, err := user.db.Exec(`INSERT INTO "user" (mxid, jid, management_room, last_connection, client_id, client_token, server_token, enc_key, mac_key) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		user.MXID, user.jidPtr(),
		user.ManagementRoom, user.LastConnection,
		sess.ClientId, sess.ClientToken, sess.ServerToken, sess.EncKey, sess.MacKey)
	if err != nil {
		user.log.Warnfln("Failed to insert %s: %v", user.MXID, err)
	}
}

func (user *User) UpdateLastConnection() {
	user.LastConnection = uint64(time.Now().Unix())
	_, err := user.db.Exec(`UPDATE "user" SET last_connection=$1 WHERE mxid=$2`,
		user.LastConnection, user.MXID)
	if err != nil {
		user.log.Warnfln("Failed to update last connection ts: %v", err)
	}
}

func (user *User) Update() {
	sess := user.sessionUnptr()
	_, err := user.db.Exec(`UPDATE "user" SET jid=$1, management_room=$2, last_connection=$3, client_id=$4, client_token=$5, server_token=$6, enc_key=$7, mac_key=$8 WHERE mxid=$9`,
		user.jidPtr(), user.ManagementRoom, user.LastConnection,
		sess.ClientId, sess.ClientToken, sess.ServerToken, sess.EncKey, sess.MacKey,
		user.MXID)
	if err != nil {
		user.log.Warnfln("Failed to update %s: %v", user.MXID, err)
	}
}

func (user *User) SetPortalKeys(newKeys []PortalKey) error {
	tx, err := user.db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM user_portal WHERE user_jid=$1", user.jidPtr())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	valueStrings := make([]string, len(newKeys))
	values := make([]interface{}, len(newKeys)*3)
	for i, key := range newKeys {
		valueStrings[i] = fmt.Sprintf("($%d, $%d, $%d)", i*3+1, i*3+2, i*3+3)
		values[i*3] = user.jidPtr()
		values[i*3+1] = key.JID
		values[i*3+2] = key.Receiver
	}
	query := fmt.Sprintf("INSERT INTO user_portal (user_jid, portal_jid, portal_receiver) VALUES %s",
		strings.Join(valueStrings, ", "))
	_, err = tx.Exec(query, values...)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (user *User) GetPortalKeys() []PortalKey {
	rows, err := user.db.Query(`SELECT portal_jid, portal_receiver FROM user_portal WHERE user_jid=$1`, user.jidPtr())
	if err != nil {
		user.log.Warnln("Failed to get user portal keys:", err)
		return nil
	}
	var keys []PortalKey
	for rows.Next() {
		var key PortalKey
		err = rows.Scan(&key.JID, &key.Receiver)
		if err != nil {
			user.log.Warnln("Failed to scan row:", err)
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
