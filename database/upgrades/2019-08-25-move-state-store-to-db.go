package upgrades

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"maunium.net/go/mautrix"
)

func init() {
	migrateRegistrations := func(tx *sql.Tx, registrations map[string]bool) error {
		if len(registrations) == 0 {
			return nil
		}

		executeBatch := func(tx *sql.Tx, valueStrings []string, values ...interface{}) error {
			valueString := strings.Join(valueStrings, ",")
			_, err := tx.Exec("INSERT INTO mx_registrations (user_id) VALUES "+valueString, values...)
			return err
		}

		batchSize := 100
		values := make([]interface{}, 0, batchSize)
		valueStrings := make([]string, 0, batchSize)
		i := 1
		for userID, registered := range registrations {
			if i == batchSize {
				err := executeBatch(tx, valueStrings, values...)
				if err != nil {
					return err
				}
				i = 1
				values = make([]interface{}, 0, batchSize)
				valueStrings = make([]string, 0, batchSize)
			}
			if registered {
				values = append(values, userID)
				valueStrings = append(valueStrings, fmt.Sprintf("($%d)", i))
				i++
			}
		}
		return executeBatch(tx, valueStrings, values...)
	}

	migrateMemberships := func(tx *sql.Tx, rooms map[string]map[string]mautrix.Membership) error {
		for roomID, members := range rooms {
			if len(members) == 0 {
				continue
			}
			var values []interface{}
			var valueStrings []string
			i := 1
			for userID, membership := range members {
				values = append(values, roomID, userID, membership)
				valueStrings = append(valueStrings, fmt.Sprintf("($%d, $%d, $%d)", i, i+1, i+2))
				i += 3
			}
			valueString := strings.Join(valueStrings, ",")
			_, err := tx.Exec("INSERT INTO mx_user_profile (room_id, user_id, membership) VALUES "+valueString, values...)
			if err != nil {
				return err
			}
		}
		return nil
	}

	migratePowerLevels := func(tx *sql.Tx, rooms map[string]*mautrix.PowerLevels) error {
		if len(rooms) == 0 {
			return nil
		}
		var values []interface{}
		var valueStrings []string
		i := 1
		for roomID, powerLevels := range rooms {
			powerLevelBytes, err := json.Marshal(powerLevels)
			if err != nil {
				return err
			}
			values = append(values, roomID, powerLevelBytes)
			valueStrings = append(valueStrings, fmt.Sprintf("($%d, $%d)", i, i+1))
			i += 2
		}
		valueString := strings.Join(valueStrings, ",")
		_, err := tx.Exec("INSERT INTO mx_room_state (room_id, power_levels) VALUES "+valueString, values...)
		return err
	}

	userProfileTable := `CREATE TABLE mx_user_profile (
		room_id     VARCHAR(255),
		user_id     VARCHAR(255),
		membership  VARCHAR(15) NOT NULL,
		PRIMARY KEY (room_id, user_id)
	)`

	roomStateTable := `CREATE TABLE mx_room_state (
		room_id      VARCHAR(255) PRIMARY KEY,
		power_levels TEXT
	)`

	registrationsTable := `CREATE TABLE mx_registrations (
		user_id VARCHAR(255) PRIMARY KEY
	)`

	type TempStateStore struct {
		Registrations map[string]bool                          `json:"registrations"`
		Members       map[string]map[string]mautrix.Membership `json:"memberships"`
		PowerLevels   map[string]*mautrix.PowerLevels          `json:"power_levels"`
	}

	upgrades[9] = upgrade{"Move state store to main DB", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == Postgres {
			roomStateTable = strings.Replace(roomStateTable, "TEXT", "JSONB", 1)
		}

		var store TempStateStore
		if _, err := tx.Exec(userProfileTable); err != nil {
			return err
		} else if _, err = tx.Exec(roomStateTable); err != nil {
			return err
		} else if _, err = tx.Exec(registrationsTable); err != nil {
			return err
		} else if data, err := ioutil.ReadFile("mx-state.json"); err != nil {
			ctx.log.Debugln("mx-state.json not found, not migrating state store")
		} else if err = json.Unmarshal(data, &store); err != nil {
			return err
		} else if err = migrateRegistrations(tx, store.Registrations); err != nil {
			return err
		} else if err = migrateMemberships(tx, store.Members); err != nil {
			return err
		} else if err = migratePowerLevels(tx, store.PowerLevels); err != nil {
			return err
		} else if err = os.Rename("mx-state.json", "mx-state.json.bak"); err != nil {
			return err
		}
		return nil
	}}
}
