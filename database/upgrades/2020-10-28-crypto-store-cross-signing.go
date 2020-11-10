package upgrades

import (
	"database/sql"

	"maunium.net/go/mautrix/crypto/sql_store_upgrade"
)

func init() {
	upgrades[19] = upgrade{"Add cross-signing keys to crypto store", func(tx *sql.Tx, c context) error {
		return sql_store_upgrade.Upgrades[3](tx, c.dialect.String())
	}}
}
