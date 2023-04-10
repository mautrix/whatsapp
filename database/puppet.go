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
	"time"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"go.mau.fi/whatsmeow/types"
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,

		EnablePresence: true,
		EnableReceipts: true,
	}
}

func (pq *PuppetQuery) GetAll() (puppets []*Puppet) {
	rows, err := pq.db.Query("SELECT username, avatar, avatar_url, displayname, name_quality, name_set, avatar_set, contact_info_set, last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts FROM puppet")
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}
	return
}

func (pq *PuppetQuery) Get(jid types.JID) *Puppet {
	row := pq.db.QueryRow("SELECT username, avatar, avatar_url, displayname, name_quality, name_set, avatar_set, contact_info_set, last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts FROM puppet WHERE username=$1", jid.User)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetByCustomMXID(mxid id.UserID) *Puppet {
	row := pq.db.QueryRow("SELECT username, avatar, avatar_url, displayname, name_quality, name_set, avatar_set, contact_info_set, last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts FROM puppet WHERE custom_mxid=$1", mxid)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetAllWithCustomMXID() (puppets []*Puppet) {
	rows, err := pq.db.Query("SELECT username, avatar, avatar_url, displayname, name_quality, name_set, avatar_set, contact_info_set, last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts FROM puppet WHERE custom_mxid<>''")
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}
	return
}

type Puppet struct {
	db  *Database
	log log.Logger

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

func (puppet *Puppet) Scan(row dbutil.Scannable) *Puppet {
	var displayname, avatar, avatarURL, customMXID, accessToken, nextBatch sql.NullString
	var quality, lastSync sql.NullInt64
	var enablePresence, enableReceipts, nameSet, avatarSet, contactInfoSet sql.NullBool
	var username string
	err := row.Scan(&username, &avatar, &avatarURL, &displayname, &quality, &nameSet, &avatarSet, &contactInfoSet, &lastSync, &customMXID, &accessToken, &nextBatch, &enablePresence, &enableReceipts)
	if err != nil {
		if err != sql.ErrNoRows {
			puppet.log.Errorln("Database scan failed:", err)
		}
		return nil
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
	return puppet
}

func (puppet *Puppet) Insert() {
	if puppet.JID.Server != types.DefaultUserServer {
		puppet.log.Warnfln("Not inserting %s: not a user", puppet.JID)
		return
	}
	var lastSyncTs int64
	if !puppet.LastSync.IsZero() {
		lastSyncTs = puppet.LastSync.Unix()
	}
	_, err := puppet.db.Exec(`
		INSERT INTO puppet (username, avatar, avatar_url, avatar_set, displayname, name_quality, name_set, contact_info_set,
							last_sync, custom_mxid, access_token, next_batch, enable_presence, enable_receipts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, puppet.JID.User, puppet.Avatar, puppet.AvatarURL.String(), puppet.AvatarSet, puppet.Displayname,
		puppet.NameQuality, puppet.NameSet, puppet.ContactInfoSet, lastSyncTs, puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch,
		puppet.EnablePresence, puppet.EnableReceipts,
	)
	if err != nil {
		puppet.log.Warnfln("Failed to insert %s: %v", puppet.JID, err)
	}
}

func (puppet *Puppet) Update() {
	var lastSyncTs int64
	if !puppet.LastSync.IsZero() {
		lastSyncTs = puppet.LastSync.Unix()
	}
	_, err := puppet.db.Exec(`
		UPDATE puppet
		SET displayname=$1, name_quality=$2, name_set=$3, avatar=$4, avatar_url=$5, avatar_set=$6, contact_info_set=$7, last_sync=$8,
		    custom_mxid=$9, access_token=$10, next_batch=$11, enable_presence=$12, enable_receipts=$13
		WHERE username=$14
	`, puppet.Displayname, puppet.NameQuality, puppet.NameSet, puppet.Avatar, puppet.AvatarURL.String(), puppet.AvatarSet, puppet.ContactInfoSet,
		lastSyncTs, puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch, puppet.EnablePresence, puppet.EnableReceipts,
		puppet.JID.User)
	if err != nil {
		puppet.log.Warnfln("Failed to update %s: %v", puppet.JID, err)
	}
}
