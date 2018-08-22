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
	"maunium.net/go/mautrix-whatsapp/types"
	"database/sql"
)

type PortalQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PortalQuery) CreateTable() error {
	_, err := pq.db.Exec(`CREATE TABLE IF NOT EXISTS portal (
		jid   VARCHAR(255),
		owner VARCHAR(255),
		mxid  VARCHAR(255) UNIQUE,

		name   VARCHAR(255),
		topic  VARCHAR(255),
		avatar VARCHAR(255),

		PRIMARY KEY (jid, owner),
		FOREIGN KEY (owner) REFERENCES user(mxid)
	)`)
	return err
}

func (pq *PortalQuery) New() *Portal {
	return &Portal{
		db:  pq.db,
		log: pq.log,
	}
}

func (pq *PortalQuery) GetAll(owner types.MatrixUserID) (portals []*Portal) {
	rows, err := pq.db.Query("SELECT * FROM portal WHERE owner=?", owner)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		portals = append(portals, pq.New().Scan(rows))
	}
	return
}

func (pq *PortalQuery) GetByJID(owner types.MatrixUserID, jid types.WhatsAppID) *Portal {
	return pq.get("SELECT * FROM portal WHERE jid=? AND owner=?", jid, owner)
}

func (pq *PortalQuery) GetByMXID(mxid types.MatrixRoomID) *Portal {
	return pq.get("SELECT * FROM portal WHERE mxid=?", mxid)
}

func (pq *PortalQuery) get(query string, args ...interface{}) *Portal {
	row := pq.db.QueryRow(query, args...)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

type Portal struct {
	db  *Database
	log log.Logger

	JID   types.WhatsAppID
	MXID  types.MatrixRoomID
	Owner types.MatrixUserID

	Name   string
	Topic  string
	Avatar string
}

func (portal *Portal) Scan(row Scannable) *Portal {
	err := row.Scan(&portal.JID, &portal.Owner, &portal.MXID, &portal.Name, &portal.Topic, &portal.Avatar)
	if err != nil {
		if err != sql.ErrNoRows {
			portal.log.Fatalln("Database scan failed:", err)
		}
		return nil
	}
	return portal
}

func (portal *Portal) Insert() error {
	var mxid *string
	if len(portal.MXID) > 0 {
		mxid = &portal.MXID
	}
	_, err := portal.db.Exec("INSERT INTO portal VALUES (?, ?, ?, ?, ?, ?)",
		portal.JID, portal.Owner, mxid, portal.Name, portal.Topic, portal.Avatar)
	if err != nil {
		portal.log.Warnfln("Failed to insert %s->%s: %v", portal.JID, portal.Owner, err)
	}
	return err
}

func (portal *Portal) Update() error {
	var mxid *string
	if len(portal.MXID) > 0 {
		mxid = &portal.MXID
	}
	_, err := portal.db.Exec("UPDATE portal SET mxid=?, name=?, topic=?, avatar=? WHERE jid=? AND owner=?",
		mxid, portal.Name, portal.Topic, portal.Avatar, portal.JID, portal.Owner)
	if err != nil {
		portal.log.Warnfln("Failed to update %s->%s: %v", portal.JID, portal.Owner, err)
	}
	return err
}
