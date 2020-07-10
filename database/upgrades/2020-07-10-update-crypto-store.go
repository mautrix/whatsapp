package upgrades

import (
	"database/sql"

	"maunium.net/go/mautrix/crypto"
)

func init() {
	upgrades[16] = upgrade{"Add account_id to crypto store", func(tx *sql.Tx, c context) error {
		return crypto.SQLStoreMigrations[1](tx, c.dialect.String())
	}}
}
