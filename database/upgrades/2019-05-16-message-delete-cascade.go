package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[1] = upgrade{"Add ON DELETE CASCADE to message table", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == SQLite {
			// SQLite doesn't support constraint updates, but it isn't that careful with constraints anyway.
			return nil
		}
		res, _ := ctx.db.Query(`SELECT EXISTS(SELECT constraint_name FROM information_schema.table_constraints
			WHERE table_name='message' AND constraint_name='message_chat_jid_fkey')`)
		var exists bool
		_ = res.Scan(&exists)
		if exists {
			_, err := tx.Exec("ALTER TABLE message DROP CONSTRAINT IF EXISTS message_chat_jid_fkey")
			if err != nil {
				return err
			}
			_, err = tx.Exec(`ALTER TABLE message ADD CONSTRAINT message_chat_jid_fkey
				FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver)
				ON DELETE CASCADE`)
			if err != nil {
				return err
			}
		}
		return nil
	}}
}
