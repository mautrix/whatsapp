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
	"fmt"
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/whatsmeow/types"
)

type PortalKey struct {
	JID      types.JID
	Receiver types.JID
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

const portalColumns = "jid, receiver, mxid, name, name_set, topic, topic_set, avatar, avatar_url, avatar_set, encrypted, last_sync, is_parent, parent_group, in_space, first_event_id, next_batch_id, relay_user_id, expiration_time"

func (pq *PortalQuery) GetAll() []*Portal {
	return pq.getAll(fmt.Sprintf("SELECT %s FROM portal", portalColumns))
}

func (pq *PortalQuery) GetByJID(key PortalKey) *Portal {
	return pq.get(fmt.Sprintf("SELECT %s FROM portal WHERE jid=$1 AND receiver=$2", portalColumns), key.JID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(mxid id.RoomID) *Portal {
	return pq.get(fmt.Sprintf("SELECT %s FROM portal WHERE mxid=$1", portalColumns), mxid)
}

func (pq *PortalQuery) GetAllByJID(jid types.JID) []*Portal {
	return pq.getAll(fmt.Sprintf("SELECT %s FROM portal WHERE jid=$1", portalColumns), jid.ToNonAD())
}

func (pq *PortalQuery) GetAllByParentGroup(jid types.JID) []*Portal {
	return pq.getAll(fmt.Sprintf("SELECT %s FROM portal WHERE parent_group=$1", portalColumns), jid)
}

func (pq *PortalQuery) FindPrivateChats(receiver types.JID) []*Portal {
	return pq.getAll(fmt.Sprintf("SELECT %s FROM portal WHERE receiver=$1 AND jid LIKE '%%@s.whatsapp.net'", portalColumns), receiver.ToNonAD())
}

func (pq *PortalQuery) FindPrivateChatsNotInSpace(receiver types.JID) (keys []PortalKey) {
	receiver = receiver.ToNonAD()
	rows, err := pq.db.Query(`
		SELECT jid FROM portal
		    LEFT JOIN user_portal ON portal.jid=user_portal.portal_jid AND portal.receiver=user_portal.portal_receiver
		WHERE mxid<>'' AND receiver=$1 AND (in_space=false OR in_space IS NULL)
	`, receiver)
	if err != nil || rows == nil {
		return
	}
	for rows.Next() {
		var key PortalKey
		key.Receiver = receiver
		err = rows.Scan(&key.JID)
		if err == nil {
			keys = append(keys, key)
		}
	}
	return
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
	NameSet   bool
	Topic     string
	TopicSet  bool
	Avatar    string
	AvatarURL id.ContentURI
	AvatarSet bool
	Encrypted bool
	LastSync  time.Time

	IsParent    bool
	ParentGroup types.JID
	InSpace     bool

	FirstEventID   id.EventID
	NextBatchID    id.BatchID
	RelayUserID    id.UserID
	ExpirationTime uint32
}

func (portal *Portal) Scan(row dbutil.Scannable) *Portal {
	var mxid, avatarURL, firstEventID, nextBatchID, relayUserID, parentGroupJID sql.NullString
	var lastSyncTs int64
	err := row.Scan(&portal.Key.JID, &portal.Key.Receiver, &mxid, &portal.Name, &portal.NameSet, &portal.Topic, &portal.TopicSet, &portal.Avatar, &avatarURL, &portal.AvatarSet, &portal.Encrypted, &lastSyncTs, &portal.IsParent, &parentGroupJID, &portal.InSpace, &firstEventID, &nextBatchID, &relayUserID, &portal.ExpirationTime)
	if err != nil {
		if err != sql.ErrNoRows {
			portal.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	if lastSyncTs > 0 {
		portal.LastSync = time.Unix(lastSyncTs, 0)
	}
	portal.MXID = id.RoomID(mxid.String)
	portal.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	if parentGroupJID.Valid {
		portal.ParentGroup, _ = types.ParseJID(parentGroupJID.String)
	}
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

func (portal *Portal) parentGroupPtr() *string {
	if !portal.ParentGroup.IsEmpty() {
		val := portal.ParentGroup.String()
		return &val
	}
	return nil
}

func (portal *Portal) lastSyncTs() int64 {
	if portal.LastSync.IsZero() {
		return 0
	}
	return portal.LastSync.Unix()
}

func (portal *Portal) Insert() {
	_, err := portal.db.Exec(`
		INSERT INTO portal (jid, receiver, mxid, name, name_set, topic, topic_set, avatar, avatar_url, avatar_set,
		                    encrypted, last_sync, is_parent, parent_group, in_space, first_event_id, next_batch_id,
		                    relay_user_id, expiration_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
	`,
		portal.Key.JID, portal.Key.Receiver, portal.mxidPtr(), portal.Name, portal.NameSet, portal.Topic, portal.TopicSet,
		portal.Avatar, portal.AvatarURL.String(), portal.AvatarSet, portal.Encrypted, portal.lastSyncTs(),
		portal.IsParent, portal.parentGroupPtr(), portal.InSpace, portal.FirstEventID.String(), portal.NextBatchID.String(),
		portal.relayUserPtr(), portal.ExpirationTime)
	if err != nil {
		portal.log.Warnfln("Failed to insert %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Update(txn dbutil.Execable) {
	if txn == nil {
		txn = portal.db
	}
	_, err := txn.Exec(`
		UPDATE portal
		SET mxid=$1, name=$2, name_set=$3, topic=$4, topic_set=$5, avatar=$6, avatar_url=$7, avatar_set=$8,
		    encrypted=$9, last_sync=$10, is_parent=$11, parent_group=$12, in_space=$13,
		    first_event_id=$14, next_batch_id=$15, relay_user_id=$16, expiration_time=$17
		WHERE jid=$18 AND receiver=$19
	`, portal.mxidPtr(), portal.Name, portal.NameSet, portal.Topic, portal.TopicSet, portal.Avatar, portal.AvatarURL.String(),
		portal.AvatarSet, portal.Encrypted, portal.lastSyncTs(), portal.IsParent, portal.parentGroupPtr(), portal.InSpace,
		portal.FirstEventID.String(), portal.NextBatchID.String(), portal.relayUserPtr(), portal.ExpirationTime,
		portal.Key.JID, portal.Key.Receiver)
	if err != nil {
		portal.log.Warnfln("Failed to update %s: %v", portal.Key, err)
	}
}

func (portal *Portal) Delete() {
	txn, err := portal.db.Begin()
	if err != nil {
		portal.log.Errorfln("Failed to begin transaction to delete portal %v: %v", portal.Key, err)
		return
	}
	defer func() {
		if err != nil {
			err = txn.Rollback()
			if err != nil {
				portal.log.Warnfln("Failed to rollback failed portal delete transaction: %v", err)
			}
		} else if err = txn.Commit(); err != nil {
			portal.log.Warnfln("Failed to commit portal delete transaction: %v", err)
		}
	}()
	_, err = portal.db.Exec("UPDATE portal SET in_space=false WHERE parent_group=$1", portal.Key.JID)
	if err != nil {
		portal.log.Warnfln("Failed to mark child groups of %v as not in space: %v", portal.Key.JID, err)
		return
	}
	_, err = portal.db.Exec("DELETE FROM portal WHERE jid=$1 AND receiver=$2", portal.Key.JID, portal.Key.Receiver)
	if err != nil {
		portal.log.Warnfln("Failed to delete %v: %v", portal.Key, err)
	}
}
