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
)

type PuppetQuery struct {
	db  *Database
	log *log.Sublogger
}

func (pq *PuppetQuery) CreateTable() error {
	_, err := pq.db.Exec(`CREATE TABLE IF NOT EXISTS puppet (
		jid      VARCHAR(255),
		receiver VARCHAR(255),

		displayname VARCHAR(255),
		avatar      VARCHAR(255),

		PRIMARY KEY(jid, receiver)
	)`)
	return err
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

func (pq *PuppetQuery) Get(jid, receiver string) *Puppet {
	row := pq.db.QueryRow("SELECT * FROM user WHERE jid=? AND receiver=?", jid, receiver)
	if row == nil {
		return nil
	}
	return pq.New().Scan(row)
}

type Puppet struct {
	db  *Database
	log *log.Sublogger

	JID      string
	Receiver string

	Displayname string
	Avatar      string
}

func (puppet *Puppet) Scan(row Scannable) *Puppet {
	err := row.Scan(&puppet.JID, &puppet.Receiver, &puppet.Displayname, &puppet.Avatar)
	if err != nil {
		puppet.log.Fatalln("Database scan failed:", err)
	}
	return puppet
}

func (puppet *Puppet) Insert() error {
	_, err := puppet.db.Exec("INSERT INTO puppet VALUES (?, ?, ?, ?)",
		puppet.JID, puppet.Receiver, puppet.Displayname, puppet.Avatar)
	return err
}

func (puppet *Puppet) Update() error {
	_, err := puppet.db.Exec("UPDATE puppet SET displayname=?, avatar=? WHERE jid=? AND receiver=?",
		puppet.Displayname, puppet.Avatar,
		puppet.JID, puppet.Receiver)
	return err
}
