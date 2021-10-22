package upgrades

import (
	"database/sql"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

func init() {
	upgrades[24] = upgrade{"Add whatsmeow state store", func(tx *sql.Tx, ctx context) error {
		return sqlstore.Upgrades[0](tx, sqlstore.NewWithDB(ctx.db, ctx.dialect.String(), nil))
	}}
}
