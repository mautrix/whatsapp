package upgrades

import (
	"database/sql"
	"fmt"
)

func init() {
	upgrades[21] = upgrade{"Add puppet activity columns", func(tx *sql.Tx, ctx context) error {
		addColumn := `ALTER TABLE puppet ADD COLUMN IF NOT EXISTS`
		if ctx.dialect == SQLite {
			addColumn = `ALTER TABLE puppet ADD COLUMN`
		}

		_, err := tx.Exec(fmt.Sprintf(`%s first_activity_ts BIGINT`, addColumn))
		if err != nil {
			return err
		}
		_, err = tx.Exec(fmt.Sprintf(`%s last_activity_ts BIGINT`, addColumn))
		return err
	}}
}
