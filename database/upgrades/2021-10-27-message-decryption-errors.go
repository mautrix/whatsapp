package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[27] = upgrade{"Add marker for WhatsApp decryption errors in message table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE message ADD COLUMN decryption_error BOOLEAN NOT NULL DEFAULT false`)
		return err
	}}
}
