package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[46] = upgrade{"Add inserted time to history sync message", func(tx *sql.Tx, ctx context) error {
		// Add the inserted time TIMESTAMP column to history_sync_message
		_, err := tx.Exec(`
			ALTER TABLE history_sync_message
			ADD COLUMN inserted_time TIMESTAMP
		`)
		return err
	}}
}
