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

	"github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	log "maunium.net/go/maulogger/v2"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"maunium.net/go/mautrix-whatsapp/database/upgrades"
)

func init() {
	sqlstore.PostgresArrayWrapper = pq.Array
}

type Database struct {
	*sql.DB
	log     log.Logger
	dialect string

	User    *UserQuery
	Portal  *PortalQuery
	Puppet  *PuppetQuery
	Message *MessageQuery
}

func New(dbType string, uri string, baseLog log.Logger) (*Database, error) {
	conn, err := sql.Open(dbType, uri)
	if err != nil {
		return nil, err
	}

	db := &Database{
		DB:      conn,
		log:     baseLog.Sub("Database"),
		dialect: dbType,
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

func (db *Database) Init() error {
	return upgrades.Run(db.log.Sub("Upgrade"), db.dialect, db.DB)
}

type Scannable interface {
	Scan(...interface{}) error
}
