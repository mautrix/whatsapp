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
		JID:      jid,
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
	return pq.get("SELECT * FROM portal WHERE jid=$1 AND receiver=$2", key.JID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid types.MatrixRoomID) *Portal {
	return pq.get("SELECT * FROM portal WHERE mxid=$1", mxid)
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

func (portal *Portal) Insert() {
	_, err := portal.db.Exec("INSERT INTO portal VALUES ($1, $2, $3, $4, $5, $6)",
		portal.Key.JID, portal.Key.Receiver, portal.mxidPtr(), portal.Name, portal.Topic, portal.Avatar)
	if err != nil {
		portal.log.Warnfln("Failed to insert %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Update() {
	var mxid *string
	if len(portal.MXID) > 0 {
		mxid = &portal.MXID
	}
	_, err := portal.db.Exec("UPDATE portal SET mxid=$1, name=$2, topic=$3, avatar=$4 WHERE jid=$5 AND receiver=$6",
		mxid, portal.Name, portal.Topic, portal.Avatar, portal.Key.JID, portal.Key.Receiver)
	if err != nil {
		portal.log.Warnfln("Failed to update %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Delete() {
	_, err := portal.db.Exec("DELETE FROM portal WHERE jid=$1 AND receiver=$2", portal.Key.JID, portal.Key.Receiver)
	if err != nil {
		portal.log.Warnfln("Failed to delete %s: %v", portal.Key, err)
	}
}

func (portal *Portal) GetUserIDs() []types.MatrixUserID {
	rows, err := portal.db.Query(`SELECT "user".mxid FROM "user", user_portal
		WHERE "user".jid=user_portal.user_jid
			AND user_portal.portal_jid=$1
			AND user_portal.portal_receiver=$2`,
		portal.Key.JID, portal.Key.Receiver)
	if err != nil {
		portal.log.Debugln("Failed to get portal user ids:", err)
		return nil
	}
	var userIDs []types.MatrixUserID
	for rows.Next() {
		var userID types.MatrixUserID
		err = rows.Scan(&userID)
		if err != nil {
			portal.log.Warnln("Failed to scan row:", err)
			continue
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs
}
