// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"

	"go.mau.fi/whatsmeow/types"
)

type PortalKey struct {
	JID      types.JID
	Receiver types.JID
}

func GroupPortalKey(jid types.JID) PortalKey {
	return NewPortalKey(jid, jid)
}

func NewPortalKey(jid, receiver types.JID) PortalKey {
	if jid.Server == types.GroupServer {
		receiver = jid
	} else if jid.Server == types.LegacyUserServer {
		jid.Server = types.DefaultUserServer
	}
	return PortalKey{
		JID:      jid.ToNonAD(),
		Receiver: receiver.ToNonAD(),
	}
}

func (key PortalKey) String() string {
	if key.Receiver == key.JID {
		return key.JID.String()
	}
	return key.JID.String() + "-" + key.Receiver.String()
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

func (pq *PortalQuery) GetAll() []*Portal {
	return pq.getAll("SELECT * FROM portal")
}

func (pq *PortalQuery) GetByJID(key PortalKey) *Portal {
	return pq.get("SELECT * FROM portal WHERE jid=$1 AND receiver=$2", key.JID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get("SELECT * FROM portal WHERE mxid=$1", mxid)
}

func (pq *PortalQuery) GetAllByJID(jid types.JID) []*Portal {
	return pq.getAll("SELECT * FROM portal WHERE jid=$1", jid.ToNonAD())
}

func (pq *PortalQuery) FindPrivateChats(receiver types.JID) []*Portal {
	return pq.getAll("SELECT * FROM portal WHERE receiver=$1 AND jid LIKE '%@s.whatsapp.net'", receiver.ToNonAD())
}

func (pq *PortalQuery) getAll(query string, args ...interface{}) (portals []*Portal) {
	rows, err := pq.db.Query(query, args...)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		portals = append(portals, pq.New().Scan(rows))
	}
	return
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
	MXID id.RoomID

	Name      string
	Topic     string
	Avatar    string
	AvatarURL id.ContentURI
	Encrypted bool

	FirstEventID id.EventID
	NextBatchID  id.BatchID

	RelayUserID id.UserID
}

func (portal *Portal) Scan(row Scannable) *Portal {
	var mxid, avatarURL, firstEventID, nextBatchID, relayUserID sql.NullString
	err := row.Scan(&portal.Key.JID, &portal.Key.Receiver, &mxid, &portal.Name, &portal.Topic, &portal.Avatar, &avatarURL, &portal.Encrypted, &firstEventID, &nextBatchID, &relayUserID)
	if err != nil {
		if err != sql.ErrNoRows {
			portal.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	portal.MXID = id.RoomID(mxid.String)
	portal.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	portal.FirstEventID = id.EventID(firstEventID.String)
	portal.NextBatchID = id.BatchID(nextBatchID.String)
	portal.RelayUserID = id.UserID(relayUserID.String)
	return portal
}

func (portal *Portal) mxidPtr() *id.RoomID {
	if len(portal.MXID) > 0 {
		return &portal.MXID
	}
	return nil
}

func (portal *Portal) relayUserPtr() *id.UserID {
	if len(portal.RelayUserID) > 0 {
		return &portal.RelayUserID
	}
	return nil
}

func (portal *Portal) Insert() {
	_, err := portal.db.Exec("INSERT INTO portal (jid, receiver, mxid, name, topic, avatar, avatar_url, encrypted, first_event_id, next_batch_id, relay_user_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		portal.Key.JID, portal.Key.Receiver, portal.mxidPtr(), portal.Name, portal.Topic, portal.Avatar, portal.AvatarURL.String(), portal.Encrypted, portal.FirstEventID.String(), portal.NextBatchID.String(), portal.relayUserPtr())
	if err != nil {
		portal.log.Warnfln("Failed to insert %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Update() {
	_, err := portal.db.Exec("UPDATE portal SET mxid=$3, name=$4, topic=$5, avatar=$6, avatar_url=$7, encrypted=$8, first_event_id=$9, next_batch_id=$10, relay_user_id=$11 WHERE jid=$1 AND receiver=$2",
		portal.Key.JID, portal.Key.Receiver, portal.mxidPtr(), portal.Name, portal.Topic, portal.Avatar, portal.AvatarURL.String(), portal.Encrypted, portal.FirstEventID.String(), portal.NextBatchID.String(), portal.relayUserPtr())
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
