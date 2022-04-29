package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[41] = upgrade{"Update backfill queue tables to be sortable by priority", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`
			UPDATE backfill_queue
				SET type=CASE
					WHEN type=1 THEN 200
					WHEN type=2 THEN 300
					ELSE type
				END
				WHERE type=1 OR type=2
		`)
		return err
	}}
}
