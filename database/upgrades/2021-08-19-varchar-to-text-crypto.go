package upgrades

import (
	"database/sql"

	"maunium.net/go/mautrix/crypto/sql_store_upgrade"
)

func init() {
	upgrades[24] = upgrade{"Replace VARCHAR(255) with TEXT in the crypto database", func(tx *sql.Tx, ctx context) error {
		return sql_store_upgrade.Upgrades[4](tx, ctx.dialect.String())
	}}
}
