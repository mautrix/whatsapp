package upgrades

import "database/sql"

func init() {
	upgrades[37] = upgrade{"Store timestamp for previous phone ping", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE "user" ADD COLUMN phone_last_pinged BIGINT`)
		return err
	}}
}
