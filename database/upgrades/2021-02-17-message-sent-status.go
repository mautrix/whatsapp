package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[20] = upgrade{"Add sent column for messages", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE message ADD COLUMN sent BOOLEAN NOT NULL DEFAULT true`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`ALTER TABLE puppet ADD COLUMN first_activity_ts BIGINT`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`ALTER TABLE puppet ADD COLUMN last_activity_ts BIGINT`)
		return err
	}}
}
