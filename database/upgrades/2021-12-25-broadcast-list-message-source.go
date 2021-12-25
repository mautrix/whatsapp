package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[32] = upgrade{"Store source broadcast list in message table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE message ADD COLUMN broadcast_list_jid TEXT`)
		return err
	}}
}
