package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[12] = upgrade{"Add encryption status to portal table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE portal ADD COLUMN encrypted BOOLEAN NOT NULL DEFAULT false`)
		return err
	}}
}
