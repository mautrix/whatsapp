package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[44] = upgrade{"Add dispatch time to backfill queue", func(tx *sql.Tx, ctx context) error {
		// First, add dispatch_time TIMESTAMP column
		_, err := tx.Exec(`
			ALTER TABLE backfill_queue
			ADD COLUMN dispatch_time TIMESTAMP
		`)
		if err != nil {
			return err
		}

		// For all previous jobs, set dispatch time to the completed time.
		_, err = tx.Exec(`
			UPDATE backfill_queue
				SET dispatch_time=completed_at
		`)
		return err
	}}
}
