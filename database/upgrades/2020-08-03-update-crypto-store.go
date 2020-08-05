package upgrades

import (
	"database/sql"

	"maunium.net/go/mautrix/crypto/sql_store_upgrade"
)

func init() {
	upgrades[18] = upgrade{"Add megolm withheld data to crypto store", func(tx *sql.Tx, c context) error {
		return sql_store_upgrade.Upgrades[2](tx, c.dialect.String())
	}}
}
