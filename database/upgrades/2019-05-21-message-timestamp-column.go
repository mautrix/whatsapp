package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[2] = upgrade{"Add timestamp column to messages", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec("ALTER TABLE message ADD COLUMN timestamp BIGINT NOT NULL DEFAULT 0")
		if err != nil {
			return err
		}
		return nil
	}}
}
