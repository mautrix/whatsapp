package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[43] = upgrade{"Add timezone column to user table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE "user" ADD COLUMN timezone TEXT`)
		return err
	}}
}
