package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[22] = upgrade{"Replace VARCHAR(255) with TEXT in the database", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == SQLite {
			// SQLite doesn't enforce varchar sizes anyway
			return nil
		}
		return execMany(tx,
			`ALTER TABLE message ALTER COLUMN chat_jid TYPE TEXT`,
			`ALTER TABLE message ALTER COLUMN chat_receiver TYPE TEXT`,
			`ALTER TABLE message ALTER COLUMN jid TYPE TEXT`,
			`ALTER TABLE message ALTER COLUMN mxid TYPE TEXT`,
			`ALTER TABLE message ALTER COLUMN sender TYPE TEXT`,

			`ALTER TABLE portal ALTER COLUMN jid TYPE TEXT`,
			`ALTER TABLE portal ALTER COLUMN receiver TYPE TEXT`,
			`ALTER TABLE portal ALTER COLUMN mxid TYPE TEXT`,
			`ALTER TABLE portal ALTER COLUMN name TYPE TEXT`,
			`ALTER TABLE portal ALTER COLUMN topic TYPE TEXT`,
			`ALTER TABLE portal ALTER COLUMN avatar TYPE TEXT`,
			`ALTER TABLE portal ALTER COLUMN avatar_url TYPE TEXT`,

			`ALTER TABLE puppet ALTER COLUMN jid TYPE TEXT`,
			`ALTER TABLE puppet ALTER COLUMN avatar TYPE TEXT`,
			`ALTER TABLE puppet ALTER COLUMN displayname TYPE TEXT`,
			`ALTER TABLE puppet ALTER COLUMN custom_mxid TYPE TEXT`,
			`ALTER TABLE puppet ALTER COLUMN access_token TYPE TEXT`,
			`ALTER TABLE puppet ALTER COLUMN next_batch TYPE TEXT`,
			`ALTER TABLE puppet ALTER COLUMN avatar_url TYPE TEXT`,

			`ALTER TABLE "user" ALTER COLUMN mxid TYPE TEXT`,
			`ALTER TABLE "user" ALTER COLUMN jid TYPE TEXT`,
			`ALTER TABLE "user" ALTER COLUMN management_room TYPE TEXT`,
			`ALTER TABLE "user" ALTER COLUMN client_id TYPE TEXT`,
			`ALTER TABLE "user" ALTER COLUMN client_token TYPE TEXT`,
			`ALTER TABLE "user" ALTER COLUMN server_token TYPE TEXT`,

			`ALTER TABLE user_portal ALTER COLUMN user_jid TYPE TEXT`,
			`ALTER TABLE user_portal ALTER COLUMN portal_jid TYPE TEXT`,
			`ALTER TABLE user_portal ALTER COLUMN portal_receiver TYPE TEXT`,
		)
	}}
}
