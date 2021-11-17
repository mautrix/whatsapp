package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[20] = upgrade{"Add sent column for messages", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE message ADD COLUMN sent BOOLEAN NOT NULL DEFAULT true`)
		return err
	}}
}
