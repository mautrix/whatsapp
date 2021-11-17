package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[21] = upgrade{"Add puppet activity columns", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE puppet ADD COLUMN IF NOT EXISTS first_activity_ts BIGINT`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE puppet ADD COLUMN IF NOT EXISTS last_activity_ts BIGINT`)
		return err
	}}
}
