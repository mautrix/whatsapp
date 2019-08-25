package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[7] = upgrade{"Add columns to store avatar MXC URIs", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE puppet ADD COLUMN avatar_url VARCHAR(255)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE portal ADD COLUMN avatar_url VARCHAR(255)`)
		if err != nil {
			return err
		}
		return nil
	}}
}
