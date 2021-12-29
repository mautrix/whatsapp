// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
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
	"encoding/json"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type SQLStateStore struct {
	*appservice.TypingStateStore

	db  *Database
	log log.Logger

	Typing     map[id.RoomID]map[id.UserID]int64
	typingLock sync.RWMutex
}

var _ appservice.StateStore = (*SQLStateStore)(nil)

func NewSQLStateStore(db *Database) *SQLStateStore {
	return &SQLStateStore{
		TypingStateStore: appservice.NewTypingStateStore(),
		db:               db,
		log:              db.log.Sub("StateStore"),
	}
}

func (store *SQLStateStore) IsRegistered(userID id.UserID) bool {
	row := store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM mx_registrations WHERE user_id=$1)", userID)
	var isRegistered bool
	err := row.Scan(&isRegistered)
	if err != nil {
		store.log.Warnfln("Failed to scan registration existence for %s: %v", userID, err)
	}
	return isRegistered
}

func (store *SQLStateStore) MarkRegistered(userID id.UserID) {
	_, err := store.db.Exec("INSERT INTO mx_registrations (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING", userID)
	if err != nil {
		store.log.Warnfln("Failed to mark %s as registered: %v", userID, err)
	}
}

func (store *SQLStateStore) GetRoomMembers(roomID id.RoomID) map[id.UserID]*event.MemberEventContent {
	members := make(map[id.UserID]*event.MemberEventContent)
	rows, err := store.db.Query("SELECT user_id, membership, displayname, avatar_url FROM mx_user_profile WHERE room_id=$1", roomID)
	if err != nil {
		return members
	}
	var userID id.UserID
	var member event.MemberEventContent
	for rows.Next() {
		err := rows.Scan(&userID, &member.Membership, &member.Displayname, &member.AvatarURL)
		if err != nil {
			store.log.Warnfln("Failed to scan member in %s: %v", roomID, err)
		} else {
			members[userID] = &member
		}
	}
	return members
}

func (store *SQLStateStore) GetMembership(roomID id.RoomID, userID id.UserID) event.Membership {
	row := store.db.QueryRow("SELECT membership FROM mx_user_profile WHERE room_id=$1 AND user_id=$2", roomID, userID)
	membership := event.MembershipLeave
	err := row.Scan(&membership)
	if err != nil && err != sql.ErrNoRows {
		store.log.Warnfln("Failed to scan membership of %s in %s: %v", userID, roomID, err)
	}
	return membership
}

func (store *SQLStateStore) GetMember(roomID id.RoomID, userID id.UserID) *event.MemberEventContent {
	member, ok := store.TryGetMember(roomID, userID)
	if !ok {
		member.Membership = event.MembershipLeave
	}
	return member
}

func (store *SQLStateStore) TryGetMember(roomID id.RoomID, userID id.UserID) (*event.MemberEventContent, bool) {
	row := store.db.QueryRow("SELECT membership, displayname, avatar_url FROM mx_user_profile WHERE room_id=$1 AND user_id=$2", roomID, userID)
	var member event.MemberEventContent
	err := row.Scan(&member.Membership, &member.Displayname, &member.AvatarURL)
	if err != nil && err != sql.ErrNoRows {
		store.log.Warnfln("Failed to scan member info of %s in %s: %v", userID, roomID, err)
	}
	return &member, err == nil
}

func (store *SQLStateStore) FindSharedRooms(userID id.UserID) (rooms []id.RoomID) {
	rows, err := store.db.Query(`
			SELECT room_id FROM mx_user_profile
			LEFT JOIN portal ON portal.mxid=mx_user_profile.room_id
			WHERE user_id=$1 AND portal.encrypted=true
	`, userID)
	if err != nil {
		store.log.Warnfln("Failed to query shared rooms with %s: %v", userID, err)
		return
	}
	for rows.Next() {
		var roomID id.RoomID
		err := rows.Scan(&roomID)
		if err != nil {
			store.log.Warnfln("Failed to scan room ID: %v", err)
		} else {
			rooms = append(rooms, roomID)
		}
	}
	return
}

func (store *SQLStateStore) IsInRoom(roomID id.RoomID, userID id.UserID) bool {
	return store.IsMembership(roomID, userID, "join")
}

func (store *SQLStateStore) IsInvited(roomID id.RoomID, userID id.UserID) bool {
	return store.IsMembership(roomID, userID, "join", "invite")
}

func (store *SQLStateStore) IsMembership(roomID id.RoomID, userID id.UserID, allowedMemberships ...event.Membership) bool {
	membership := store.GetMembership(roomID, userID)
	for _, allowedMembership := range allowedMemberships {
		if allowedMembership == membership {
			return true
		}
	}
	return false
}

func (store *SQLStateStore) SetMembership(roomID id.RoomID, userID id.UserID, membership event.Membership) {
	_, err := store.db.Exec(`
		INSERT INTO mx_user_profile (room_id, user_id, membership) VALUES ($1, $2, $3)
		ON CONFLICT (room_id, user_id) DO UPDATE SET membership=excluded.membership
	`, roomID, userID, membership)
	if err != nil {
		store.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, membership, err)
	}
}

func (store *SQLStateStore) SetMember(roomID id.RoomID, userID id.UserID, member *event.MemberEventContent) {
	_, err := store.db.Exec(`
		INSERT INTO mx_user_profile (room_id, user_id, membership, displayname, avatar_url) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (room_id, user_id) DO UPDATE SET membership=excluded.membership, displayname=excluded.displayname, avatar_url=excluded.avatar_url
	`, roomID, userID, member.Membership, member.Displayname, member.AvatarURL)
	if err != nil {
		store.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, member, err)
	}
}

func (store *SQLStateStore) SetPowerLevels(roomID id.RoomID, levels *event.PowerLevelsEventContent) {
	levelsBytes, err := json.Marshal(levels)
	if err != nil {
		store.log.Errorfln("Failed to marshal power levels of %s: %v", roomID, err)
		return
	}
	_, err = store.db.Exec(`
		INSERT INTO mx_room_state (room_id, power_levels) VALUES ($1, $2)
		ON CONFLICT (room_id) DO UPDATE SET power_levels=excluded.power_levels
	`, roomID, levelsBytes)
	if err != nil {
		store.log.Warnfln("Failed to store power levels of %s: %v", roomID, err)
	}
}

func (store *SQLStateStore) GetPowerLevels(roomID id.RoomID) (levels *event.PowerLevelsEventContent) {
	row := store.db.QueryRow("SELECT power_levels FROM mx_room_state WHERE room_id=$1", roomID)
	if row == nil {
		return
	}
	var data []byte
	err := row.Scan(&data)
	if err != nil {
		store.log.Errorfln("Failed to scan power levels of %s: %v", roomID, err)
		return
	}
	levels = &event.PowerLevelsEventContent{}
	err = json.Unmarshal(data, levels)
	if err != nil {
		store.log.Errorfln("Failed to parse power levels of %s: %v", roomID, err)
		return nil
	}
	return
}

func (store *SQLStateStore) GetPowerLevel(roomID id.RoomID, userID id.UserID) int {
	if store.db.dialect == "postgres" {
		row := store.db.QueryRow(`SELECT
			COALESCE((power_levels->'users'->$2)::int, (power_levels->'users_default')::int, 0)
			FROM mx_room_state WHERE room_id=$1`, roomID, userID)
		if row == nil {
			// Power levels not in db
			return 0
		}
		var powerLevel int
		err := row.Scan(&powerLevel)
		if err != nil {
			store.log.Errorfln("Failed to scan power level of %s in %s: %v", userID, roomID, err)
		}
		return powerLevel
	}
	return store.GetPowerLevels(roomID).GetUserLevel(userID)
}

func (store *SQLStateStore) GetPowerLevelRequirement(roomID id.RoomID, eventType event.Type) int {
	if store.db.dialect == "postgres" {
		defaultType := "events_default"
		defaultValue := 0
		if eventType.IsState() {
			defaultType = "state_default"
			defaultValue = 50
		}
		row := store.db.QueryRow(`SELECT
			COALESCE((power_levels->'events'->$2)::int, (power_levels->'$3')::int, $4)
			FROM mx_room_state WHERE room_id=$1`, roomID, eventType.Type, defaultType, defaultValue)
		if row == nil {
			// Power levels not in db
			return defaultValue
		}
		var powerLevel int
		err := row.Scan(&powerLevel)
		if err != nil {
			store.log.Errorfln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
		}
		return powerLevel
	}
	return store.GetPowerLevels(roomID).GetEventLevel(eventType)
}

func (store *SQLStateStore) HasPowerLevel(roomID id.RoomID, userID id.UserID, eventType event.Type) bool {
	if store.db.dialect == "postgres" {
		defaultType := "events_default"
		defaultValue := 0
		if eventType.IsState() {
			defaultType = "state_default"
			defaultValue = 50
		}
		row := store.db.QueryRow(`SELECT
			COALESCE((power_levels->'users'->$2)::int, (power_levels->'users_default')::int, 0)
			>=
			COALESCE((power_levels->'events'->$3)::int, (power_levels->'$4')::int, $5)
			FROM mx_room_state WHERE room_id=$1`, roomID, userID, eventType.Type, defaultType, defaultValue)
		if row == nil {
			// Power levels not in db
			return defaultValue == 0
		}
		var hasPower bool
		err := row.Scan(&hasPower)
		if err != nil {
			store.log.Errorfln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
		}
		return hasPower
	}
	return store.GetPowerLevel(roomID, userID) >= store.GetPowerLevelRequirement(roomID, eventType)
}
