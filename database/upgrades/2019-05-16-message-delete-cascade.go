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
		_, err := tx.Exec("ALTER TABLE message DROP CONSTRAINT message_chat_jid_fkey")
		if err != nil {
			return err
		}
		_, err = tx.Exec(`ALTER TABLE message ADD CONSTRAINT message_chat_jid_fkey
				FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver)
				ON DELETE CASCADE`)
		if err != nil {
			return err
		}
		return nil
	}}
}
