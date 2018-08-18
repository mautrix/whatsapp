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
	_ "github.com/mattn/go-sqlite3"
	log "maunium.net/go/maulogger"
)

type Database struct {
	*sql.DB
	log log.Logger

	User   *UserQuery
	Portal *PortalQuery
	Puppet *PuppetQuery
}

func New(file string) (*Database, error) {
	conn, err := sql.Open("sqlite3", file)
	if err != nil {
		return nil, err
	}

	db := &Database{
		DB:  conn,
		log: log.Sub("Database"),
	}
	db.User = &UserQuery{
		db:  db,
		log: db.log.Sub("User"),
	}
	db.Portal = &PortalQuery{
		db:  db,
		log: db.log.Sub("Portal"),
	}
	db.Puppet = &PuppetQuery{
		db:  db,
		log: db.log.Sub("Puppet"),
	}
	return db, nil
}

func (db *Database) CreateTables() error {
	err := db.User.CreateTable()
	if err != nil {
		return err
	}
	err = db.Portal.CreateTable()
	if err != nil {
		return err
	}
	err = db.Puppet.CreateTable()
	if err != nil {
		return err
	}
	return nil
}

type Scannable interface {
	Scan(...interface{}) error
}
