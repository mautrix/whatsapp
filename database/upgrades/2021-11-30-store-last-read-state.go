package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[30] = upgrade{"Store last read message timestamp in database", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`CREATE TABLE user_portal (
			user_mxid       TEXT,
			portal_jid      TEXT,
			portal_receiver TEXT,

			last_read_ts    BIGINT NOT NULL DEFAULT 0,

			PRIMARY KEY (user_mxid, portal_jid, portal_receiver),
			FOREIGN KEY (user_mxid) REFERENCES "user"(mxid) ON DELETE CASCADE ON UPDATE CASCADE,
			FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE ON UPDATE CASCADE
		)`)
		return err
	}}
}
