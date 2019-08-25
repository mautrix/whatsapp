package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[5] = upgrade{"Add columns to store custom puppet info", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE puppet ADD COLUMN custom_mxid VARCHAR(255)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE puppet ADD COLUMN access_token VARCHAR(1023)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE puppet ADD COLUMN next_batch VARCHAR(255)`)
		if err != nil {
			return err
		}
		return nil
	}}
}
