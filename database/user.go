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
	"strings"

	"github.com/Rhymen/go-whatsapp"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) CreateTable() error {
	_, err := uq.db.Exec(`CREATE TABLE IF NOT EXISTS user (
		mxid VARCHAR(255) PRIMARY KEY,
		jid  VARCHAR(25)  UNIQUE,

		management_room VARCHAR(255),

		client_id    VARCHAR(255),
		client_token VARCHAR(255),
		server_token VARCHAR(255),
		enc_key      BLOB,
		mac_key      BLOB
	)`)
	return err
}

func (uq *UserQuery) New() *User {
	return &User{
		db:  uq.db,
		log: uq.log,
	}
}

func (uq *UserQuery) GetAll() (users []*User) {
	rows, err := uq.db.Query("SELECT * FROM user")
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
	row := uq.db.QueryRow("SELECT * FROM user WHERE mxid=?", userID)
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

func (uq *UserQuery) GetByJID(userID types.WhatsAppID) *User {
	row := uq.db.QueryRow("SELECT * FROM user WHERE jid=?", stripSuffix(userID))
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
}

func (user *User) Scan(row Scannable) *User {
	var managementRoom, clientID, clientToken, serverToken, jid sql.NullString
	var encKey, macKey []byte
	err := row.Scan(&user.MXID, &jid, &managementRoom, &clientID, &clientToken, &serverToken, &encKey, &macKey)
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

func (user *User) sessionUnptr() (sess whatsapp.Session) {
	if user.Session != nil {
		sess = *user.Session
	}
	return
}

func (user *User) Insert() error {
	sess := user.sessionUnptr()
	_, err := user.db.Exec("INSERT INTO user VALUES (?, ?, ?, ?, ?, ?, ?, ?)", user.MXID, stripSuffix(user.JID),
		user.ManagementRoom,
		sess.ClientId, sess.ClientToken, sess.ServerToken, sess.EncKey, sess.MacKey)
	return err
}

func (user *User) Update() error {
	sess := user.sessionUnptr()
	_, err := user.db.Exec("UPDATE user SET jid=?, management_room=?, client_id=?, client_token=?, server_token=?, enc_key=?, mac_key=? WHERE mxid=?",
		stripSuffix(user.JID), user.ManagementRoom,
		sess.ClientId, sess.ClientToken, sess.ServerToken, sess.EncKey, sess.MacKey,
		user.MXID)
	return err
}
