package upgrades

import (
	"database/sql"
	"strings"

	"maunium.net/go/mautrix/event"
)

func init() {
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
		Registrations map[string]bool                           `json:"registrations"`
		Members       map[string]map[string]event.Membership    `json:"memberships"`
		PowerLevels   map[string]*event.PowerLevelsEventContent `json:"power_levels"`
	}

	upgrades[9] = upgrade{"Move state store to main DB", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == Postgres {
			roomStateTable = strings.Replace(roomStateTable, "TEXT", "JSONB", 1)
		}

		if _, err := tx.Exec(userProfileTable); err != nil {
			return err
		} else if _, err = tx.Exec(roomStateTable); err != nil {
			return err
		} else if _, err = tx.Exec(registrationsTable); err != nil {
			return err
		}
		return nil
	}}
}
