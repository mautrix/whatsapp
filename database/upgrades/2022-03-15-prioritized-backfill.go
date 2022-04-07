package upgrades

import (
	"database/sql"
	"fmt"
)

func init() {
	upgrades[39] = upgrade{"Add backfill queue", func(tx *sql.Tx, ctx context) error {
		// The queue_id needs to auto-increment every insertion. For SQLite,
		// INTEGER PRIMARY KEY is an alias for the ROWID, so it will
		// auto-increment. See https://sqlite.org/lang_createtable.html#rowid
		// For Postgres, we need to add GENERATED ALWAYS AS IDENTITY for the
		// same functionality.
		queueIDColumnTypeModifier := ""
		if ctx.dialect == Postgres {
			queueIDColumnTypeModifier = "GENERATED ALWAYS AS IDENTITY"
		}

		_, err := tx.Exec(fmt.Sprintf(`
			CREATE TABLE backfill_queue (
				queue_id            INTEGER PRIMARY KEY %s,
				user_mxid           TEXT,
				type                INTEGER NOT NULL,
				priority            INTEGER NOT NULL,
				portal_jid          TEXT,
				portal_receiver     TEXT,
				time_start          TIMESTAMP,
				time_end            TIMESTAMP,
				max_batch_events    INTEGER NOT NULL,
				max_total_events    INTEGER,
				batch_delay         INTEGER,
				completed_at        TIMESTAMP,

				FOREIGN KEY (user_mxid) REFERENCES "user"(mxid) ON DELETE CASCADE ON UPDATE CASCADE,
				FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
			)
		`, queueIDColumnTypeModifier))
		if err != nil {
			return err
		}

		return err
	}}
}
