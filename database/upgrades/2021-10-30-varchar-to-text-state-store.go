package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[29] = upgrade{"Replace VARCHAR(255) with TEXT in the Matrix state store", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == SQLite {
			// SQLite doesn't enforce varchar sizes anyway
			return nil
		}
		return execMany(tx,
			`ALTER TABLE mx_registrations ALTER COLUMN user_id TYPE TEXT`,
			`ALTER TABLE mx_room_state ALTER COLUMN room_id TYPE TEXT`,
			`ALTER TABLE mx_user_profile ALTER COLUMN room_id TYPE TEXT`,
			`ALTER TABLE mx_user_profile ALTER COLUMN user_id TYPE TEXT`,
			`ALTER TABLE mx_user_profile ALTER COLUMN membership TYPE TEXT`,
			`ALTER TABLE mx_user_profile ALTER COLUMN avatar_url TYPE TEXT`,
		)
	}}
}
