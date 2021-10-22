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

type UserQuery struct {
	db  *Database
	log log.Logger
}

func (uq *UserQuery) New() *User {
	return &User{
		db:  uq.db,
		log: uq.log,
	}
}

func (uq *UserQuery) GetAll() (users []*User) {
	rows, err := uq.db.Query(`SELECT mxid, username, agent, device, management_room FROM "user"`)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		users = append(users, uq.New().Scan(rows))
	}
	return
}

func (uq *UserQuery) GetByMXID(userID id.UserID) *User {
	row := uq.db.QueryRow(`SELECT mxid, username, agent, device, management_room FROM "user" WHERE mxid=$1`, userID)
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

func (uq *UserQuery) GetByUsername(username string) *User {
	row := uq.db.QueryRow(`SELECT mxid, username, agent, device, management_room FROM "user" WHERE username=$1`, username)
	if row == nil {
		return nil
	}
	return uq.New().Scan(row)
}

type User struct {
	db  *Database
	log log.Logger

	MXID           id.UserID
	JID            types.JID
	ManagementRoom id.RoomID
}

func (user *User) Scan(row Scannable) *User {
	var username sql.NullString
	var device, agent sql.NullByte
	err := row.Scan(&user.MXID, &username, &agent, &device, &user.ManagementRoom)
	if err != nil {
		if err != sql.ErrNoRows {
			user.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	if len(username.String) > 0 {
		user.JID = types.NewADJID(username.String, agent.Byte, device.Byte)
	}
	return user
}

func (user *User) usernamePtr() *string {
	if !user.JID.IsEmpty() {
		return &user.JID.User
	}
	return nil
}

func (user *User) agentPtr() *uint8 {
	if !user.JID.IsEmpty() {
		return &user.JID.Agent
	}
	return nil
}

func (user *User) devicePtr() *uint8 {
	if !user.JID.IsEmpty() {
		return &user.JID.Device
	}
	return nil
}

func (user *User) Insert() {
	_, err := user.db.Exec(`INSERT INTO "user" (mxid, username, agent, device, management_room) VALUES ($1, $2, $3, $4, $5)`,
		user.MXID, user.usernamePtr(), user.agentPtr(), user.devicePtr(), user.ManagementRoom)
	if err != nil {
		user.log.Warnfln("Failed to insert %s: %v", user.MXID, err)
	}
}

func (user *User) Update() {
	_, err := user.db.Exec(`UPDATE "user" SET username=$1, agent=$2, device=$3, management_room=$4 WHERE mxid=$5`,
		user.usernamePtr(), user.agentPtr(), user.devicePtr(), user.ManagementRoom, user.MXID)
	if err != nil {
		user.log.Warnfln("Failed to update %s: %v", user.MXID, err)
	}
}

//type PortalKeyWithMeta struct {
//	PortalKey
//	InCommunity bool
//}
//
//func (user *User) SetPortalKeys(newKeys []PortalKeyWithMeta) error {
//	tx, err := user.db.Begin()
//	if err != nil {
//		return err
//	}
//	_, err = tx.Exec("DELETE FROM user_portal WHERE user_jid=$1", user.jidPtr())
//	if err != nil {
//		_ = tx.Rollback()
//		return err
//	}
//	valueStrings := make([]string, len(newKeys))
//	values := make([]interface{}, len(newKeys)*4)
//	for i, key := range newKeys {
//		pos := i * 4
//		valueStrings[i] = fmt.Sprintf("($%d, $%d, $%d, $%d)", pos+1, pos+2, pos+3, pos+4)
//		values[pos] = user.jidPtr()
//		values[pos+1] = key.JID
//		values[pos+2] = key.Receiver
//		values[pos+3] = key.InCommunity
//	}
//	query := fmt.Sprintf("INSERT INTO user_portal (user_jid, portal_jid, portal_receiver, in_community) VALUES %s",
//		strings.Join(valueStrings, ", "))
//	_, err = tx.Exec(query, values...)
//	if err != nil {
//		_ = tx.Rollback()
//		return err
//	}
//	return tx.Commit()
//}
//
//func (user *User) IsInPortal(key PortalKey) bool {
//	row := user.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM user_portal WHERE user_jid=$1 AND portal_jid=$2 AND portal_receiver=$3)`, user.jidPtr(), &key.JID, &key.Receiver)
//	var exists bool
//	_ = row.Scan(&exists)
//	return exists
//}
//
//func (user *User) GetPortalKeys() []PortalKey {
//	rows, err := user.db.Query(`SELECT portal_jid, portal_receiver FROM user_portal WHERE user_jid=$1`, user.jidPtr())
//	if err != nil {
//		user.log.Warnln("Failed to get user portal keys:", err)
//		return nil
//	}
//	var keys []PortalKey
//	for rows.Next() {
//		var key PortalKey
//		err = rows.Scan(&key.JID, &key.Receiver)
//		if err != nil {
//			user.log.Warnln("Failed to scan row:", err)
//			continue
//		}
//		keys = append(keys, key)
//	}
//	return keys
//}
//
//func (user *User) GetInCommunityMap() map[PortalKey]bool {
//	rows, err := user.db.Query(`SELECT portal_jid, portal_receiver, in_community FROM user_portal WHERE user_jid=$1`, user.jidPtr())
//	if err != nil {
//		user.log.Warnln("Failed to get user portal keys:", err)
//		return nil
//	}
//	keys := make(map[PortalKey]bool)
//	for rows.Next() {
//		var key PortalKey
//		var inCommunity bool
//		err = rows.Scan(&key.JID, &key.Receiver, &inCommunity)
//		if err != nil {
//			user.log.Warnln("Failed to scan row:", err)
//			continue
//		}
//		keys[key] = inCommunity
//	}
//	return keys
//}
//
//func (user *User) CreateUserPortal(newKey PortalKeyWithMeta) {
//	user.log.Debugfln("Creating new portal %s for %s", newKey.PortalKey.JID, newKey.PortalKey.Receiver)
//	_, err := user.db.Exec(`INSERT INTO user_portal (user_jid, portal_jid, portal_receiver, in_community) VALUES ($1, $2, $3, $4)`,
//		user.jidPtr(),
//		newKey.PortalKey.JID, newKey.PortalKey.Receiver,
//		newKey.InCommunity)
//	if err != nil {
//		user.log.Warnfln("Failed to insert %s: %v", user.MXID, err)
//	}
//}
