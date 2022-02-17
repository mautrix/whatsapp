package upgrades

import "database/sql"

func init() {
	upgrades[36] = upgrade{"Store message error type as string", func(tx *sql.Tx, ctx context) error {
		if ctx.dialect == Postgres {
			_, err := tx.Exec("CREATE TYPE error_type AS ENUM ('', 'decryption_failed', 'media_not_found')")
			if err != nil {
				return err
			}
		}
		_, err := tx.Exec("ALTER TABLE message ADD COLUMN error error_type NOT NULL DEFAULT ''")
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE message SET error='decryption_failed' WHERE decryption_error=true")
		if err != nil {
			return err
		}
		if ctx.dialect == Postgres {
			// TODO do this on sqlite at some point
			_, err = tx.Exec("ALTER TABLE message DROP COLUMN decryption_error")
			if err != nil {
				return err
			}
		}
		return nil
	}}
}
