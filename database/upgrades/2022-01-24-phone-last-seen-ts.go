package upgrades

import "database/sql"

func init() {
	upgrades[36] = upgrade{"Store approximate last seen timestamp of the main device", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE "user" ADD COLUMN phone_last_seen BIGINT`)
		return err
	}}
}
