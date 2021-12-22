package upgrades

import (
	"database/sql"

	"maunium.net/go/mautrix/crypto/sql_store_upgrade"
)

func init() {
	upgrades[31] = upgrade{"Split last_used into last_encrypted and last_decrypted in crypto store", func(tx *sql.Tx, c context) error {
		return sql_store_upgrade.Upgrades[5](tx, c.dialect.String())
	}}
}
