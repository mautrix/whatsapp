package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[21] = upgrade{"Remove message content from local database", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == SQLite {
			_, err := tx.Exec("ALTER TABLE message RENAME TO old_message")
			if err != nil {
				return err
			}
			_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS message (
				chat_jid      TEXT,
				chat_receiver TEXT,
				jid           TEXT,
				mxid          TEXT    NOT NULL UNIQUE,
				sender        TEXT    NOT NULL,
				timestamp     BIGINT  NOT NULL,
				sent          BOOLEAN NOT NULL,

				PRIMARY KEY (chat_jid, chat_receiver, jid),
				FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
			)`)
			if err != nil {
				return err
			}
			_, err = tx.Exec("INSERT INTO message SELECT chat_jid, chat_receiver, jid, mxid, sender, timestamp, sent FROM old_message")
			return err
		} else {
			_, err := tx.Exec(`ALTER TABLE message DROP COLUMN content`)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`ALTER TABLE message ALTER COLUMN timestamp DROP DEFAULT`)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`ALTER TABLE message ALTER COLUMN sent DROP DEFAULT`)
			return err
		}
	}}
}
