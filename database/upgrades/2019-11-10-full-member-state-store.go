package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[10] = upgrade{"Add columns to store full member info in state store", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE mx_user_profile ADD COLUMN displayname TEXT`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE mx_user_profile ADD COLUMN avatar_url VARCHAR(255)`)
		return err
	}}
}
