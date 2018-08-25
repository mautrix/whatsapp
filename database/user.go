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
	"github.com/Rhymen/go-whatsapp"
	"maunium.net/go/mautrix-whatsapp/types"
	"database/sql"
)

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) CreateTable() error {
	_, err := uq.db.Exec(`CREATE TABLE IF NOT EXISTS user (
		mxid VARCHAR(255) PRIMARY KEY,

		management_room VARCHAR(255),

		client_id    VARCHAR(255),
		client_token VARCHAR(255),
		server_token VARCHAR(255),
		enc_key      BLOB,
		mac_key      BLOB,
		wid          VARCHAR(255)
	)`)
	return err
}

func (uq *UserQuery) New() *User {
	return &User{
		db:     uq.db,
		log:    uq.log,
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

func (uq *UserQuery) Get(userID types.MatrixUserID) *User {
	row := uq.db.QueryRow("SELECT * FROM user WHERE mxid=?", userID)
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

type User struct {
	db  *Database
	log log.Logger

	ID             types.MatrixUserID
	ManagementRoom types.MatrixRoomID
	Session        *whatsapp.Session
}

func (user *User) Scan(row Scannable) *User {
	sess := whatsapp.Session{}
	err := row.Scan(&user.ID, &user.ManagementRoom, &sess.ClientId, &sess.ClientToken, &sess.ServerToken,
		&sess.EncKey, &sess.MacKey, &sess.Wid)
	if err != nil {
		if err != sql.ErrNoRows {
			user.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	if len(sess.ClientId) > 0 {
		user.Session = &sess
	} else {
		user.Session = nil
	}
	return user
}

func (user *User) Insert() error {
	var sess whatsapp.Session
	if user.Session != nil {
		sess = *user.Session
	}
	_, err := user.db.Exec("INSERT INTO user VALUES (?, ?, ?, ?, ?, ?, ?, ?)", user.ID, user.ManagementRoom,
		sess.ClientId, sess.ClientToken, sess.ServerToken, sess.EncKey, sess.MacKey, sess.Wid)
	return err
}

func (user *User) Update() error {
	var sess whatsapp.Session
	if user.Session != nil {
		sess = *user.Session
	}
	_, err := user.db.Exec("UPDATE user SET management_room=?, client_id=?, client_token=?, server_token=?, enc_key=?, mac_key=?, wid=? WHERE mxid=?",
		user.ManagementRoom,
		sess.ClientId, sess.ClientToken, sess.ServerToken, sess.EncKey, sess.MacKey, sess.Wid, user.ID)
	return err
}
