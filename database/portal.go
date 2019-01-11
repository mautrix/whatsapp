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

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix-whatsapp/types"
)

type PortalKey struct {
	JID      types.WhatsAppID
	Receiver types.WhatsAppID
}

func GroupPortalKey(jid types.WhatsAppID) PortalKey {
	return PortalKey{
		JID:      jid,
		Receiver: jid,
	}
}

func NewPortalKey(jid, receiver types.WhatsAppID) PortalKey {
	if strings.HasSuffix(jid, "@g.us") {
		receiver = jid
	}
	return PortalKey{
		JID: jid,
		Receiver: receiver,
	}
}

func (key PortalKey) String() string {
	if key.Receiver == key.JID {
		return key.JID
	}
	return key.JID + "-" + key.Receiver
}

type PortalQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PortalQuery) CreateTable() error {
	_, err := pq.db.Exec(`CREATE TABLE IF NOT EXISTS portal (
		jid      VARCHAR(25),
		receiver VARCHAR(25),
		mxid     VARCHAR(255) UNIQUE,

		name   VARCHAR(255) NOT NULL,
		topic  VARCHAR(255) NOT NULL,
		avatar VARCHAR(255) NOT NULL,

		PRIMARY KEY (jid, receiver),
		FOREIGN KEY (receiver) REFERENCES user(mxid)
	)`)
	return err
}

func (pq *PortalQuery) New() *Portal {
	return &Portal{
		db:  pq.db,
		log: pq.log,
	}
}

func (pq *PortalQuery) GetAll() (portals []*Portal) {
	rows, err := pq.db.Query("SELECT * FROM portal")
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		portals = append(portals, pq.New().Scan(rows))
	}
	return
}

func (pq *PortalQuery) GetByJID(key PortalKey) *Portal {
	return pq.get("SELECT * FROM portal WHERE jid=? AND receiver=?", key.JID, key.Receiver)
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

	Key  PortalKey
	MXID types.MatrixRoomID

	Name   string
	Topic  string
	Avatar string
}

func (portal *Portal) Scan(row Scannable) *Portal {
	var mxid sql.NullString
	err := row.Scan(&portal.Key.JID, &portal.Key.Receiver, &mxid, &portal.Name, &portal.Topic, &portal.Avatar)
	if err != nil {
		if err != sql.ErrNoRows {
			portal.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	portal.MXID = mxid.String
	return portal
}

func (portal *Portal) mxidPtr() *string {
	if len(portal.MXID) > 0 {
		return &portal.MXID
	}
	return nil
}

func (portal *Portal) Insert() error {
	_, err := portal.db.Exec("INSERT INTO portal VALUES (?, ?, ?, ?, ?, ?)",
		portal.Key.JID, portal.Key.Receiver, portal.mxidPtr(), portal.Name, portal.Topic, portal.Avatar)
	if err != nil {
		portal.log.Warnfln("Failed to insert %s: %v", portal.Key, err)
	}
	return err
}

func (portal *Portal) Update() error {
	var mxid *string
	if len(portal.MXID) > 0 {
		mxid = &portal.MXID
	}
	_, err := portal.db.Exec("UPDATE portal SET mxid=?, name=?, topic=?, avatar=? WHERE jid=? AND receiver=?",
		mxid, portal.Name, portal.Topic, portal.Avatar, portal.Key.JID, portal.Key.Receiver)
	if err != nil {
		portal.log.Warnfln("Failed to update %s: %v", portal.Key, err)
	}
	return err
}
