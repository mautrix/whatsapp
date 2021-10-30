package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[25] = upgrade{"Update things for multidevice", func(tx *sql.Tx, ctx context) error {
		// This is probably not necessary
		_, err := tx.Exec("DROP TABLE user_portal")
		if err != nil {
			return err
		}

		// Remove invalid puppet rows
		_, err = tx.Exec("DELETE FROM puppet WHERE jid LIKE '%@g.us' OR jid LIKE '%@broadcast'")
		if err != nil {
			return err
		}
		// Remove the suffix from puppets since they'll all have the same suffix
		_, err = tx.Exec("UPDATE puppet SET jid=REPLACE(jid, '@s.whatsapp.net', '')")
		if err != nil {
			return err
		}
		// Rename column to correctly represent the new content
		_, err = tx.Exec("ALTER TABLE puppet RENAME COLUMN jid TO username")
		if err != nil {
			return err
		}

		if ctx.dialect == SQLite {
			// Message content was removed from the main message table earlier, but the backup table still exists for SQLite
			_, err = tx.Exec("DROP TABLE IF EXISTS old_message")

			_, err = tx.Exec(`ALTER TABLE "user" RENAME TO old_user`)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`CREATE TABLE "user" (
				mxid     TEXT PRIMARY KEY,
				username TEXT UNIQUE,
				agent    SMALLINT,
				device   SMALLINT,
				management_room TEXT
			)`)
			if err != nil {
				return err
			}

			// No need to copy auth data, users need to relogin anyway
			_, err = tx.Exec(`INSERT INTO "user" (mxid, management_room) SELECT mxid, management_room FROM old_user`)
			if err != nil {
				return err
			}

			_, err = tx.Exec("DROP TABLE old_user")
			if err != nil {
				return err
			}
		} else {
			// The jid column never actually contained the full JID, so let's rename it.
			_, err = tx.Exec(`ALTER TABLE "user" RENAME COLUMN jid TO username`)
			if err != nil {
				return err
			}

			// The auth data is now in the whatsmeow_device table.
			for _, column := range []string{"last_connection", "client_id", "client_token", "server_token", "enc_key", "mac_key"} {
				_, err = tx.Exec(`ALTER TABLE "user" DROP COLUMN ` + column)
				if err != nil {
					return err
				}
			}

			// The whatsmeow_device table is keyed by the full JID, so we need to store the other parts of the JID here too.
			_, err = tx.Exec(`ALTER TABLE "user" ADD COLUMN agent SMALLINT`)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`ALTER TABLE "user" ADD COLUMN device SMALLINT`)
			if err != nil {
				return err
			}

			// Clear all usernames, the users need to relogin anyway.
			_, err = tx.Exec(`UPDATE "user" SET username=null`)
			if err != nil {
				return err
			}
		}
		return nil
	}}
}
