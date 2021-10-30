package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[28] = upgrade{"Add relay user field to portal table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE portal ADD COLUMN relay_user_id TEXT`)
		return err
	}}
}
