// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"context"
	"database/sql"
	"time"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/dbutil"
)

type PortalKey struct {
	JID      types.JID
	Receiver types.JID
}

func NewPortalKey(jid, receiver types.JID) PortalKey {
	if jid.Server == types.GroupServer || jid.Server == types.NewsletterServer {
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
	*dbutil.QueryHelper[*Portal]
}

func newPortal(qh *dbutil.QueryHelper[*Portal]) *Portal {
	return &Portal{
		qh: qh,
	}
}

const (
	getAllPortalsQuery = `
		SELECT jid, receiver, mxid, name, name_set, topic, topic_set, avatar, avatar_url, avatar_set,
		       encrypted, last_sync, is_parent, parent_group, in_space,
		       first_event_id, next_batch_id, relay_user_id, expiration_time
		FROM portal
	`
	getPortalByJIDQuery                   = getAllPortalsQuery + " WHERE jid=$1 AND receiver=$2"
	getPortalByMXIDQuery                  = getAllPortalsQuery + " WHERE mxid=$1"
	getPrivateChatsWithQuery              = getAllPortalsQuery + " WHERE jid=$1"
	getPrivateChatsOfQuery                = getAllPortalsQuery + " WHERE receiver=$1"
	getAllPortalsByParentGroupQuery       = getAllPortalsQuery + " WHERE parent_group=$1"
	findPrivateChatPortalsNotInSpaceQuery = `
		SELECT jid FROM portal
		    LEFT JOIN user_portal ON portal.jid=user_portal.portal_jid AND portal.receiver=user_portal.portal_receiver
		WHERE mxid<>'' AND receiver=$1 AND (user_portal.in_space=false OR user_portal.in_space IS NULL)
	`

	insertPortalQuery = `
		INSERT INTO portal (
			jid, receiver, mxid, name, name_set, topic, topic_set, avatar, avatar_url, avatar_set,
			encrypted, last_sync, is_parent, parent_group, in_space,
			first_event_id, next_batch_id, relay_user_id, expiration_time
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
	`
	updatePortalQuery = `
		UPDATE portal
		SET mxid=$3, name=$4, name_set=$5, topic=$6, topic_set=$7, avatar=$8, avatar_url=$9, avatar_set=$10,
		    encrypted=$11, last_sync=$12, is_parent=$13, parent_group=$14, in_space=$15,
		    first_event_id=$16, next_batch_id=$17, relay_user_id=$18, expiration_time=$19
		WHERE jid=$1 AND receiver=$2
	`
	clearPortalInSpaceQuery = "UPDATE portal SET in_space=false WHERE parent_group=$1"
	deletePortalQuery       = "DELETE FROM portal WHERE jid=$1 AND receiver=$2"
)

func (pq *PortalQuery) GetAll(ctx context.Context) ([]*Portal, error) {
	return pq.QueryMany(ctx, getAllPortalsQuery)
}

func (pq *PortalQuery) GetByJID(ctx context.Context, key PortalKey) (*Portal, error) {
	return pq.QueryOne(ctx, getPortalByJIDQuery, key.JID, key.Receiver)
}

func (pq *PortalQuery) GetByMXID(ctx context.Context, mxid id.RoomID) (*Portal, error) {
	return pq.QueryOne(ctx, getPortalByMXIDQuery, mxid)
}

func (pq *PortalQuery) GetAllByJID(ctx context.Context, jid types.JID) ([]*Portal, error) {
	return pq.QueryMany(ctx, getPrivateChatsWithQuery, jid.ToNonAD())
}

func (pq *PortalQuery) FindPrivateChats(ctx context.Context, receiver types.JID) ([]*Portal, error) {
	return pq.QueryMany(ctx, getPrivateChatsOfQuery, receiver.ToNonAD())
}

func (pq *PortalQuery) GetAllByParentGroup(ctx context.Context, jid types.JID) ([]*Portal, error) {
	return pq.QueryMany(ctx, getAllPortalsByParentGroupQuery, jid)
}

func (pq *PortalQuery) FindPrivateChatsNotInSpace(ctx context.Context, receiver types.JID) (keys []PortalKey, err error) {
	receiver = receiver.ToNonAD()
	scanFn := func(rows dbutil.Scannable) (key PortalKey, err error) {
		key.Receiver = receiver
		err = rows.Scan(&key.JID)
		return
	}
	return dbutil.ConvertRowFn[PortalKey](scanFn).
		NewRowIter(pq.GetDB().Query(ctx, findPrivateChatPortalsNotInSpaceQuery, receiver)).
		AsList()
}

type Portal struct {
	qh *dbutil.QueryHelper[*Portal]

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

func (portal *Portal) Scan(row dbutil.Scannable) (*Portal, error) {
	var mxid, avatarURL, firstEventID, nextBatchID, relayUserID, parentGroupJID sql.NullString
	var lastSyncTs int64
	err := row.Scan(
		&portal.Key.JID, &portal.Key.Receiver, &mxid, &portal.Name, &portal.NameSet,
		&portal.Topic, &portal.TopicSet, &portal.Avatar, &avatarURL, &portal.AvatarSet, &portal.Encrypted,
		&lastSyncTs, &portal.IsParent, &parentGroupJID, &portal.InSpace,
		&firstEventID, &nextBatchID, &relayUserID, &portal.ExpirationTime,
	)
	if err != nil {
		return nil, err
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
	return portal, nil
}

func (portal *Portal) sqlVariables() []any {
	var lastSyncTS int64
	if !portal.LastSync.IsZero() {
		lastSyncTS = portal.LastSync.Unix()
	}
	return []any{
		portal.Key.JID, portal.Key.Receiver, dbutil.StrPtr(portal.MXID), portal.Name, portal.NameSet,
		portal.Topic, portal.TopicSet, portal.Avatar, portal.AvatarURL.String(), portal.AvatarSet, portal.Encrypted,
		lastSyncTS, portal.IsParent, dbutil.StrPtr(portal.ParentGroup.String()), portal.InSpace,
		portal.FirstEventID.String(), portal.NextBatchID.String(), dbutil.StrPtr(portal.RelayUserID), portal.ExpirationTime,
	}
}

func (portal *Portal) Insert(ctx context.Context) error {
	return portal.qh.Exec(ctx, insertPortalQuery, portal.sqlVariables()...)
}

func (portal *Portal) Update(ctx context.Context) error {
	return portal.qh.Exec(ctx, updatePortalQuery, portal.sqlVariables()...)
}

func (portal *Portal) Delete(ctx context.Context) error {
	return portal.qh.GetDB().DoTxn(ctx, nil, func(ctx context.Context) error {
		err := portal.qh.Exec(ctx, clearPortalInSpaceQuery, portal.Key.JID)
		if err != nil {
			return err
		}
		return portal.qh.Exec(ctx, deletePortalQuery, portal.Key.JID, portal.Key.Receiver)
	})
}
