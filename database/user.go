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
)

type UserQuery struct {
	db  *Database
	log *log.Sublogger
}

func (uq *UserQuery) CreateTable() error {
	_, err := uq.db.Exec(`CREATE TABLE IF NOT EXISTS user (
		mxid  VARCHAR(255) PRIMARY KEY,

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

func (uq *UserQuery) Get(userID string) *User {
	row := uq.db.QueryRow("SELECT * FROM user WHERE mxid=?", userID)
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

type User struct {
	db  *Database
	log *log.Sublogger

	UserID string

	session whatsapp.Session
}

func (user *User) Scan(row Scannable) *User {
	err := row.Scan(&user.UserID, &user.session.ClientId, &user.session.ClientToken, &user.session.ServerToken,
		&user.session.EncKey, &user.session.MacKey, &user.session.Wid)
	if err != nil {
		user.log.Fatalln("Database scan failed:", err)
	}
	return user
}

func (user *User) Insert() error {
	_, err := user.db.Exec("INSERT INTO user VALUES (?, ?, ?, ?, ?, ?, ?)", user.UserID, user.session.ClientId,
		user.session.ClientToken, user.session.ServerToken, user.session.EncKey, user.session.MacKey, user.session.Wid)
	return err
}

func (user *User) Update() error {
	_, err := user.db.Exec("UPDATE user SET client_id=?, client_token=?, server_token=?, enc_key=?, mac_key=?, wid=? WHERE mxid=?",
		user.session.ClientId, user.session.ClientToken, user.session.ServerToken, user.session.EncKey, user.session.MacKey,
		user.session.Wid, user.UserID)
	return err
}
