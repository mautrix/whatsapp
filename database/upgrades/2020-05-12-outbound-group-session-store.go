package upgrades

import (
	"database/sql"
)

func init() {
	upgrades[14] = upgrade{"Add outbound group sessions to database", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`CREATE TABLE crypto_megolm_outbound_session (
			room_id       VARCHAR(255) PRIMARY KEY,
			session_id    CHAR(43)     NOT NULL UNIQUE,
			session       bytea        NOT NULL,
			shared        BOOLEAN      NOT NULL,
			max_messages  INTEGER      NOT NULL,
			message_count INTEGER      NOT NULL,
			max_age       BIGINT       NOT NULL,
			created_at    timestamp    NOT NULL,
			last_used     timestamp    NOT NULL
		)`)
		if err != nil {
			return err
		}
		return nil
	}}
}
