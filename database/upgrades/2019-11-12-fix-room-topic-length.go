package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[11] = upgrade{"Adjust the length of column topic in portal", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == SQLite {
			// SQLite doesn't support constraint updates, but it isn't that careful with constraints anyway.
			return nil
		}
		_, err := tx.Exec(`ALTER TABLE portal ALTER COLUMN topic TYPE VARCHAR(512)`)
		return err
	}}
}
