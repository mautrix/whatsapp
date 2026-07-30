package upgrades

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestMatrixRTCReactionUpgradeSQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, statement := range []string{
		`CREATE TABLE user_login (
			bridge_id TEXT NOT NULL,
			id TEXT NOT NULL,
			PRIMARY KEY (bridge_id, id)
		)`,
		`CREATE TABLE portal (
			bridge_id TEXT NOT NULL,
			id TEXT NOT NULL,
			receiver TEXT NOT NULL,
			PRIMARY KEY (bridge_id, id, receiver)
		)`,
	} {
		if _, err = db.ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"10-matrixrtc-call.sql", "11-matrixrtc-reactions.sql"} {
		script, readErr := rawUpgrades.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.ExecContext(context.Background(), string(script)); err != nil {
			t.Fatalf("%s failed: %v", name, err)
		}
	}

	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info(whatsapp_matrixrtc_call)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, name := range []string{
		"bridge_membership_event_id",
		"selected_membership_event_id",
		"bridge_hand_raise_event_id",
		"selected_hand_raise_event_id",
	} {
		if !columns[name] {
			t.Fatalf("upgraded MatrixRTC call table is missing %s", name)
		}
	}
}
