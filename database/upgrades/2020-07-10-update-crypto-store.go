package upgrades

import (
	"database/sql"

	"maunium.net/go/mautrix/crypto/sql_store_upgrade"
)

func init() {
	upgrades[16] = upgrade{"Add account_id to crypto store", func(tx *sql.Tx, c context) error {
		return sql_store_upgrade.Upgrades[1](tx, c.dialect.String())
	}}
}
