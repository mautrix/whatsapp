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

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/dbutil"
)

type PuppetQuery struct {
	*dbutil.QueryHelper[*Puppet]
}

func newPuppet(qh *dbutil.QueryHelper[*Puppet]) *Puppet {
	return &Puppet{
		qh: qh,

		EnablePresence: true,
		EnableReceipts: true,
	}
}

const (
	getAllPuppetsQuery = `
		SELECT username, avatar, avatar_url, displayname, name_quality, name_set, avatar_set, contact_info_set,
		       last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts
		FROM puppet
	`
	getPuppetByJIDQuery              = getAllPuppetsQuery + " WHERE username=$1"
	getPuppetByCustomMXIDQuery       = getAllPuppetsQuery + " WHERE custom_mxid=$1"
	getAllPuppetsWithCustomMXIDQuery = getAllPuppetsQuery + " WHERE custom_mxid<>''"
	insertPuppetQuery                = `
		INSERT INTO puppet (username, avatar, avatar_url, avatar_set, displayname, name_quality, name_set, contact_info_set,
							last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	updatePuppetQuery = `
		UPDATE puppet
		SET avatar=$2, avatar_url=$3, avatar_set=$4, displayname=$5, name_quality=$6, name_set=$7, contact_info_set=$8,
		    last_sync=$9, custom_mxid=$10, access_token=$11, next_batch=$12, enable_presence=$13, enable_receipts=$14
		WHERE username=$1
	`
)

func (pq *PuppetQuery) GetAll(ctx context.Context) ([]*Puppet, error) {
	return pq.QueryMany(ctx, getAllPuppetsQuery)
}

func (pq *PuppetQuery) Get(ctx context.Context, jid types.JID) (*Puppet, error) {
	return pq.QueryOne(ctx, getPuppetByJIDQuery, jid.User)
}

func (pq *PuppetQuery) GetByCustomMXID(ctx context.Context, mxid id.UserID) (*Puppet, error) {
	return pq.QueryOne(ctx, getPuppetByCustomMXIDQuery, mxid)
}

func (pq *PuppetQuery) GetAllWithCustomMXID(ctx context.Context) ([]*Puppet, error) {
	return pq.QueryMany(ctx, getAllPuppetsWithCustomMXIDQuery)
}

type Puppet struct {
	qh *dbutil.QueryHelper[*Puppet]

	JID            types.JID
	Avatar         string
	AvatarURL      id.ContentURI
	AvatarSet      bool
	Displayname    string
	NameQuality    int8
	NameSet        bool
	ContactInfoSet bool
	LastSync       time.Time

	CustomMXID     id.UserID
	AccessToken    string
	NextBatch      string
	EnablePresence bool
	EnableReceipts bool
}

func (puppet *Puppet) Scan(row dbutil.Scannable) (*Puppet, error) {
	var displayname, avatar, avatarURL, customMXID, accessToken, nextBatch sql.NullString
	var quality, lastSync sql.NullInt64
	var enablePresence, enableReceipts, nameSet, avatarSet, contactInfoSet sql.NullBool
	var username string
	err := row.Scan(&username, &avatar, &avatarURL, &displayname, &quality, &nameSet, &avatarSet, &contactInfoSet, &lastSync, &customMXID, &accessToken, &nextBatch, &enablePresence, &enableReceipts)
	if err != nil {
		return nil, err
	}
	puppet.JID = types.NewJID(username, types.DefaultUserServer)
	puppet.Displayname = displayname.String
	puppet.Avatar = avatar.String
	puppet.AvatarURL, _ = id.ParseContentURI(avatarURL.String)
	puppet.NameQuality = int8(quality.Int64)
	puppet.NameSet = nameSet.Bool
	puppet.AvatarSet = avatarSet.Bool
	puppet.ContactInfoSet = contactInfoSet.Bool
	if lastSync.Int64 > 0 {
		puppet.LastSync = time.Unix(lastSync.Int64, 0)
	}
	puppet.CustomMXID = id.UserID(customMXID.String)
	puppet.AccessToken = accessToken.String
	puppet.NextBatch = nextBatch.String
	puppet.EnablePresence = enablePresence.Bool
	puppet.EnableReceipts = enableReceipts.Bool
	return puppet, nil
}

func (puppet *Puppet) sqlVariables() []any {
	var lastSyncTS int64
	if !puppet.LastSync.IsZero() {
		lastSyncTS = puppet.LastSync.Unix()
	}
	return []any{
		puppet.JID.User, puppet.Avatar, puppet.AvatarURL.String(), puppet.AvatarSet, puppet.Displayname,
		puppet.NameQuality, puppet.NameSet, puppet.ContactInfoSet, lastSyncTS,
		puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch,
		puppet.EnablePresence, puppet.EnableReceipts,
	}
}

func (puppet *Puppet) Insert(ctx context.Context) error {
	if puppet.JID.Server != types.DefaultUserServer {
		zerolog.Ctx(ctx).Warn().Stringer("jid", puppet.JID).Msg("Not inserting puppet: not a user")
		return nil
	}
	return puppet.qh.Exec(ctx, insertPuppetQuery, puppet.sqlVariables()...)
}

func (puppet *Puppet) Update(ctx context.Context) error {
	return puppet.qh.Exec(ctx, updatePuppetQuery, puppet.sqlVariables()...)
}
