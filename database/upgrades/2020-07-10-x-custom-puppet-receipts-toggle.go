package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[17] = upgrade{"Add enable_receipts column for puppets", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE puppet ADD COLUMN enable_receipts BOOLEAN NOT NULL DEFAULT true`)
		return err
	}}
}
