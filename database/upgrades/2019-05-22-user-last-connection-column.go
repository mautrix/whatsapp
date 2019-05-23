package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[3] = upgrade{"Add last_connection column to users", func(dialect Dialect, tx *sql.Tx, db *sql.DB) error {
		_, err := tx.Exec(`ALTER TABLE "user" ADD COLUMN last_connection BIGINT NOT NULL DEFAULT 0`)
		if err != nil {
			return err
		}
		return nil
	}}
}
