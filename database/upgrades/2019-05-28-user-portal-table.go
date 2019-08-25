package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[6] = upgrade{"Add user-portal mapping table", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`CREATE TABLE user_portal (
			user_jid        VARCHAR(255),
			portal_jid      VARCHAR(255),
			portal_receiver VARCHAR(255),
			PRIMARY KEY (user_jid, portal_jid, portal_receiver),
			FOREIGN KEY (user_jid)                    REFERENCES "user"(jid)           ON DELETE CASCADE,
			FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
		)`)
		return err
	}}
}
