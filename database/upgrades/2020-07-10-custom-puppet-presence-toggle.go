package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[15] = upgrade{"Add enable_presence column for puppets", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE puppet ADD COLUMN enable_presence BOOLEAN NOT NULL DEFAULT true`)
		return err
	}}
}
