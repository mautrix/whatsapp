package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[26] = upgrade{"Add columns to store infinite backfill pointers for portals", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE portal ADD COLUMN first_event_id TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE portal ADD COLUMN next_batch_id TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return err
		}
		return nil
	}}
}
