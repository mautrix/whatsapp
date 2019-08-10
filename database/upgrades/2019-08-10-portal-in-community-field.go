package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[8] = upgrade{"Add columns to store portal in filtering community meta", func(dialect Dialect, tx *sql.Tx, db *sql.DB) error {
		_, err := tx.Exec(`ALTER TABLE user_portal ADD COLUMN in_community BOOLEAN NOT NULL DEFAULT FALSE`)
		return err
	}}
}
