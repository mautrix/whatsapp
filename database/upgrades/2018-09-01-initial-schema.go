package upgrades

import (
	"database/sql"
	"fmt"
)

func init() {
	upgrades[0] = upgrade{"Initial schema", func(dialect Dialect, tx *sql.Tx, db *sql.DB) error {
		var byteType string
		if dialect == SQLite {
			byteType = "BLOB"
		} else {
			byteType = "bytea"
		}
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS portal (
			jid      VARCHAR(255),
			receiver VARCHAR(255),
			mxid     VARCHAR(255) UNIQUE,
	
			name   VARCHAR(255) NOT NULL,
			topic  VARCHAR(255) NOT NULL,
			avatar VARCHAR(255) NOT NULL,
	
			PRIMARY KEY (jid, receiver)
		)`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS puppet (
			jid          VARCHAR(255) PRIMARY KEY,
			avatar       VARCHAR(255),
			displayname  VARCHAR(255),
			name_quality SMALLINT
		)`)
		if err != nil {
			return err
		}

		_, err = tx.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "user" (
			mxid VARCHAR(255) PRIMARY KEY,
			jid  VARCHAR(255) UNIQUE,

			management_room VARCHAR(255),

			client_id    VARCHAR(255),
			client_token VARCHAR(255),
			server_token VARCHAR(255),
			enc_key      %[1]s,
			mac_key      %[1]s
		)`, byteType))
		if err != nil {
			return err
		}

		_, err = tx.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS message (
			chat_jid      VARCHAR(255),
			chat_receiver VARCHAR(255),
			jid           VARCHAR(255),
			mxid          VARCHAR(255) NOT NULL UNIQUE,
			sender        VARCHAR(255) NOT NULL,
			content       %[1]s        NOT NULL,

			PRIMARY KEY (chat_jid, chat_receiver, jid),
			FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
		)`, byteType))
		if err != nil {
			return err
		}

		return nil
	}}
}
