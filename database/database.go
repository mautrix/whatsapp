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

	_ "github.com/lib/pq"

	log "maunium.net/go/maulogger/v2"
)

type Database struct {
	*sql.DB
	log log.Logger

	User    *UserQuery
	Portal  *PortalQuery
	Puppet  *PuppetQuery
	Message *MessageQuery
}

func New(dbType string, uri string) (*Database, error) {
	conn, err := sql.Open(dbType, uri)
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
	db.Message = &MessageQuery{
		db:  db,
		log: db.log.Sub("Message"),
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
	err = db.Message.CreateTable()
	if err != nil {
		return err
	}
	return nil
}

type Scannable interface {
	Scan(...interface{}) error
}
