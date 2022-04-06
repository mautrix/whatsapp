package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[40] = upgrade{"Store history syncs for later backfills", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`
			CREATE TABLE history_sync_conversation (
				user_mxid                       TEXT,
				conversation_id                 TEXT,
				portal_jid                      TEXT,
				portal_receiver                 TEXT,
				last_message_timestamp          TIMESTAMP,
				archived                        BOOLEAN,
				pinned                          INTEGER,
				mute_end_time                   TIMESTAMP,
				disappearing_mode               INTEGER,
				end_of_history_transfer_type    INTEGER,
				ephemeral_expiration            INTEGER,
				marked_as_unread                BOOLEAN,
				unread_count                    INTEGER,

				PRIMARY KEY (user_mxid, conversation_id),
				FOREIGN KEY (user_mxid) REFERENCES "user"(mxid) ON DELETE CASCADE ON UPDATE CASCADE,
				FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE ON UPDATE CASCADE
			)
		`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`
			CREATE TABLE history_sync_message (
				user_mxid                TEXT,
				conversation_id          TEXT,
				message_id               TEXT,
				timestamp                TIMESTAMP,
				data                     BYTEA,

				PRIMARY KEY (user_mxid, conversation_id, message_id),
				FOREIGN KEY (user_mxid) REFERENCES "user"(mxid) ON DELETE CASCADE ON UPDATE CASCADE,
				FOREIGN KEY (user_mxid, conversation_id) REFERENCES history_sync_conversation(user_mxid, conversation_id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return err
		}

		return nil
	}}
}
