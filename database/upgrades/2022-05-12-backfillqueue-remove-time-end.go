package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[45] = upgrade{"Add dispatch time to backfill queue", func(tx *sql.Tx, ctx context) error {
		// First, add dispatch_time TIMESTAMP column
		_, err := tx.Exec(`
			ALTER TABLE backfill_queue
			DROP COLUMN time_end
		`)
		return err
	}}
}
