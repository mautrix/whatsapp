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
	"encoding/json"
	"fmt"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix-appservice"
)

type SQLStateStore struct {
	*appservice.TypingStateStore

	db  *Database
	log log.Logger

	Typing     map[string]map[string]int64
	typingLock sync.RWMutex
}

func NewSQLStateStore(db *Database) *SQLStateStore {
	return &SQLStateStore{
		TypingStateStore: appservice.NewTypingStateStore(),
		db:               db,
		log:              log.Sub("StateStore"),
	}
}

func (store *SQLStateStore) IsRegistered(userID string) bool {
	row := store.db.QueryRow("SELECT EXISTS(SELECT 1 FROM mx_registrations WHERE user_id=$1)", userID)
	var isRegistered bool
	err := row.Scan(&isRegistered)
	if err != nil {
		store.log.Warnfln("Failed to scan registration existence for %s: %v", userID, err)
	}
	return isRegistered
}

func (store *SQLStateStore) MarkRegistered(userID string) {
	var err error
	if store.db.dialect == "postgres" {
		_, err = store.db.Exec("INSERT INTO mx_registrations (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING", userID)
	} else if store.db.dialect == "sqlite3" {
		_, err = store.db.Exec("INSERT OR REPLACE INTO mx_registrations (user_id) VALUES ($1)", userID)
	} else {
		err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	}
	if err != nil {
		store.log.Warnfln("Failed to mark %s as registered: %v", userID, err)
	}
}

func (store *SQLStateStore) GetRoomMemberships(roomID string) map[string]mautrix.Membership {
	memberships := make(map[string]mautrix.Membership)
	rows, err := store.db.Query("SELECT user_id, membership FROM mx_user_profile WHERE room_id=$1", roomID)
	if err != nil {
		return memberships
	}
	var userID string
	var membership mautrix.Membership
	for rows.Next() {
		err := rows.Scan(&userID, &membership)
		if err != nil {
			store.log.Warnfln("Failed to scan membership in %s: %v", roomID, err)
		} else {
			memberships[userID] = membership
		}
	}
	return memberships
}

func (store *SQLStateStore) GetMembership(roomID, userID string) mautrix.Membership {
	row := store.db.QueryRow("SELECT membership FROM mx_user_profile WHERE room_id=$1 AND user_id=$2", roomID, userID)
	membership := mautrix.MembershipLeave
	if row != nil && row.Scan(&membership) != nil {
		store.log.Warnln("Failed to scan membership of %s in %s: %v", userID, roomID, membership)
	}
	return membership
}

func (store *SQLStateStore) IsInRoom(roomID, userID string) bool {
	return store.IsMembership(roomID, userID, "join")
}

func (store *SQLStateStore) IsInvited(roomID, userID string) bool {
	return store.IsMembership(roomID, userID, "join", "invite")
}

func (store *SQLStateStore) IsMembership(roomID, userID string, allowedMemberships ...mautrix.Membership) bool {
	membership := store.GetMembership(roomID, userID)
	for _, allowedMembership := range allowedMemberships {
		if allowedMembership == membership {
			return true
		}
	}
	return false
}
func (store *SQLStateStore) SetMembership(roomID, userID string, membership mautrix.Membership) {
	var err error
	if store.db.dialect == "postgres" {
		_, err = store.db.Exec(`INSERT INTO mx_user_profile (room_id, user_id, membership) VALUES ($1, $2, $3)
			ON CONFLICT (room_id, user_id) DO UPDATE SET membership=$3`, roomID, userID, membership)
	} else if store.db.dialect == "sqlite3" {
		_, err = store.db.Exec("INSERT OR REPLACE INTO mx_user_profile (room_id, user_id, membership) VALUES ($1, $2, $3)", roomID, userID, membership)
	} else {
		err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	}
	if err != nil {
		store.log.Warnfln("Failed to set membership of %s in %s to %s: %v", userID, roomID, membership, err)
	}
}

func (store *SQLStateStore) SetPowerLevels(roomID string, levels *mautrix.PowerLevels) {
	levelsBytes, err := json.Marshal(levels)
	if err != nil {
		store.log.Errorfln("Failed to marshal power levels of %s: %v", roomID, err)
		return
	}
	if store.db.dialect == "postgres" {
		_, err = store.db.Exec(`INSERT INTO mx_room_state (room_id, power_levels) VALUES ($1, $2)
			ON CONFLICT (room_id) DO UPDATE SET power_levels=$2`, roomID, levelsBytes)
	} else if store.db.dialect == "sqlite3" {
		_, err = store.db.Exec("INSERT OR REPLACE INTO mx_room_state (room_id, power_levels) VALUES ($1, $2)", roomID, levelsBytes)
	} else {
		err = fmt.Errorf("unsupported dialect %s", store.db.dialect)
	}
	if err != nil {
		store.log.Warnfln("Failed to store power levels of %s: %v", roomID, err)
	}
}

func (store *SQLStateStore) GetPowerLevels(roomID string) (levels *mautrix.PowerLevels) {
	row := store.db.QueryRow("SELECT power_levels FROM mx_room_state WHERE room_id=$1", roomID)
	if row == nil {
		return
	}
	var data []byte
	err := row.Scan(&data)
	if err != nil {
		store.log.Errorln("Failed to scan power levels of %s: %v", roomID, err)
		return
	}
	levels = &mautrix.PowerLevels{}
	err = json.Unmarshal(data, levels)
	if err != nil {
		store.log.Errorln("Failed to parse power levels of %s: %v", roomID, err)
		return nil
	}
	return
}

func (store *SQLStateStore) GetPowerLevel(roomID, userID string) int {
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
			store.log.Errorln("Failed to scan power level of %s in %s: %v", userID, roomID, err)
		}
		return powerLevel
	}
	return store.GetPowerLevels(roomID).GetUserLevel(userID)
}

func (store *SQLStateStore) GetPowerLevelRequirement(roomID string, eventType mautrix.EventType) int {
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
			store.log.Errorln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
		}
		return powerLevel
	}
	return store.GetPowerLevels(roomID).GetEventLevel(eventType)
}

func (store *SQLStateStore) HasPowerLevel(roomID, userID string, eventType mautrix.EventType) bool {
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
			store.log.Errorln("Failed to scan power level for %s in %s: %v", eventType, roomID, err)
		}
		return hasPower
	}
	return store.GetPowerLevel(roomID, userID) >= store.GetPowerLevelRequirement(roomID, eventType)
}
