package upgrades

import "database/sql"

func init() {
	upgrades[38] = upgrade{"Add support for reactions", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE message ADD COLUMN type TEXT NOT NULL DEFAULT 'message'`)
		if err != nil {
			return err
		}
		if ctx.dialect == Postgres {
			_, err = tx.Exec("ALTER TABLE message ALTER COLUMN type DROP DEFAULT")
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec("UPDATE message SET type='' WHERE error='decryption_failed'")
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE message SET type='fake' WHERE jid LIKE 'FAKE::%' OR mxid LIKE 'net.maunium.whatsapp.fake::%' OR jid=mxid")
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE reaction (
			chat_jid      TEXT,
			chat_receiver TEXT,
			target_jid    TEXT,
			sender        TEXT,
			mxid          TEXT NOT NULL,
			jid           TEXT NOT NULL,
			PRIMARY KEY (chat_jid, chat_receiver, target_jid, sender),
			CONSTRAINT target_message_fkey FOREIGN KEY (chat_jid, chat_receiver, target_jid)
				REFERENCES message(chat_jid, chat_receiver, jid)
				ON DELETE CASCADE ON UPDATE CASCADE
		)`)
		return err
	}}
}
