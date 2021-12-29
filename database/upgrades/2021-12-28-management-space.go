package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[32] = upgrade{"Store space in user table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE "user" ADD COLUMN space_room TEXT NOT NULL DEFAULT ''`)
		return err
	}}
}
