package upgrades

import "database/sql"

func init() {
	upgrades[34] = upgrade{"Add support for disappearing messages", func(tx *sql.Tx, ctx context) error {
		_, err := tx.Exec(`ALTER TABLE portal ADD COLUMN expiration_time BIGINT NOT NULL DEFAULT 0 CHECK (expiration_time >= 0 AND expiration_time < 4294967296)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`CREATE TABLE disappearing_message (
			room_id   TEXT,
			event_id  TEXT,
			expire_in BIGINT NOT NULL,
			expire_at BIGINT,
			PRIMARY KEY (room_id, event_id)
		)`)
		return err
	}}
}
