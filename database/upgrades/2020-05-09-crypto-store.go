package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[13] = upgrade{"Add crypto store to database", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`CREATE TABLE crypto_account (
			device_id  VARCHAR(255) PRIMARY KEY,
			shared     BOOLEAN      NOT NULL,
			sync_token TEXT         NOT NULL,
			account    bytea        NOT NULL
		)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE crypto_message_index (
			sender_key CHAR(43),
			session_id CHAR(43),
			"index"    INTEGER,
			event_id   VARCHAR(255) NOT NULL,
			timestamp  BIGINT       NOT NULL,

			PRIMARY KEY (sender_key, session_id, "index")
		)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE crypto_tracked_user (
			user_id VARCHAR(255) PRIMARY KEY
		)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE crypto_device (
			user_id      VARCHAR(255),
			device_id    VARCHAR(255),
			identity_key CHAR(43)      NOT NULL,
			signing_key  CHAR(43)      NOT NULL,
			trust        SMALLINT      NOT NULL,
			deleted      BOOLEAN       NOT NULL,
			name         VARCHAR(255)  NOT NULL,

			PRIMARY KEY (user_id, device_id)
		)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE crypto_olm_session (
			session_id   CHAR(43)  PRIMARY KEY,
			sender_key   CHAR(43)  NOT NULL,
			session      bytea     NOT NULL,
			created_at   timestamp NOT NULL,
			last_used    timestamp NOT NULL
		)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE crypto_megolm_inbound_session (
			session_id   CHAR(43)     PRIMARY KEY,
			sender_key   CHAR(43)     NOT NULL,
			signing_key  CHAR(43)     NOT NULL,
			room_id      VARCHAR(255) NOT NULL,
			session      bytea        NOT NULL,
			forwarding_chains bytea   NOT NULL
		)`)
		if err != nil {
			return err
		}
		return nil
	}}
}
