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

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix-whatsapp/types"
)

type PuppetQuery struct {
	db  *Database
	log log.Logger
}

func (pq *PuppetQuery) New() *Puppet {
	return &Puppet{
		db:  pq.db,
		log: pq.log,
	}
}

func (pq *PuppetQuery) GetAll() (puppets []*Puppet) {
	rows, err := pq.db.Query("SELECT * FROM puppet")
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		puppets = append(puppets, pq.New().Scan(rows))
	}
	return
}

func (pq *PuppetQuery) Get(jid types.WhatsAppID) *Puppet {
	row := pq.db.QueryRow("SELECT * FROM puppet WHERE jid=$1", jid)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetByCustomMXID(mxid types.MatrixUserID) *Puppet {
	row := pq.db.QueryRow("SELECT * FROM puppet WHERE custom_mxid=$1", mxid)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

func (pq *PuppetQuery) GetAllWithCustomMXID() (puppets []*Puppet) {
	rows, err := pq.db.Query("SELECT * FROM puppet WHERE custom_mxid<>''")
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

	JID         types.WhatsAppID
	Avatar      string
	Displayname string
	NameQuality int8

	CustomMXID  string
	AccessToken string
	NextBatch   string
}

func (puppet *Puppet) Scan(row Scannable) *Puppet {
	var displayname, avatar, customMXID, accessToken, nextBatch sql.NullString
	var quality sql.NullInt64
	err := row.Scan(&puppet.JID, &avatar, &displayname, &quality, &customMXID, &accessToken, &nextBatch)
	if err != nil {
		if err != sql.ErrNoRows {
			puppet.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	puppet.Displayname = displayname.String
	puppet.Avatar = avatar.String
	puppet.NameQuality = int8(quality.Int64)
	puppet.CustomMXID = customMXID.String
	puppet.AccessToken = accessToken.String
	puppet.NextBatch = nextBatch.String
	return puppet
}

func (puppet *Puppet) Insert() {
	_, err := puppet.db.Exec("INSERT INTO puppet VALUES ($1, $2, $3, $4, $5, $6, $7)",
		puppet.JID, puppet.Avatar, puppet.Displayname, puppet.NameQuality, puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch)
	if err != nil {
		puppet.log.Warnfln("Failed to insert %s: %v", puppet.JID, err)
	}
}

func (puppet *Puppet) Update() {
	_, err := puppet.db.Exec("UPDATE puppet SET displayname=$1, name_quality=$2, avatar=$3, custom_mxid=$4, access_token=$5, next_batch=$6 WHERE jid=$7",
		puppet.Displayname, puppet.NameQuality, puppet.Avatar, puppet.CustomMXID, puppet.AccessToken, puppet.NextBatch, puppet.JID)
	if err != nil {
		puppet.log.Warnfln("Failed to update %s->%s: %v", puppet.JID, err)
	}
}
