package upgrades

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	log "maunium.net/go/maulogger/v2"
)

type Dialect int

const (
	Postgres Dialect = iota
	SQLite
)

func (dialect Dialect) String() string {
	switch dialect {
	case Postgres:
		return "postgres"
	case SQLite:
		return "sqlite3"
	default:
		return ""
	}
}

type upgradeFunc func(*sql.Tx, context) error

type context struct {
	dialect Dialect
	db      *sql.DB
	log     log.Logger
}

type upgrade struct {
	message string
	fn      upgradeFunc
}

const NumberOfUpgrades = 47

var upgrades [NumberOfUpgrades]upgrade

var ErrUnsupportedDatabaseVersion = fmt.Errorf("unsupported database schema version")
var ErrForeignTables = fmt.Errorf("the database contains foreign tables")
var ErrNotOwned = fmt.Errorf("the database is owned by")
var IgnoreForeignTables = false

const databaseOwner = "mautrix-whatsapp"

func GetVersion(db *sql.DB) (int, error) {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS version (version INTEGER)")
	if err != nil {
		return -1, err
	}

	version := 0
	err = db.QueryRow("SELECT version FROM version LIMIT 1").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return -1, err
	}
	return version, nil
}

const tableExistsPostgres = "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name=$1)"
const tableExistsSQLite = "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND table_name=$1)"

func tableExists(dialect Dialect, db *sql.DB, table string) (exists bool) {
	if dialect == SQLite {
		_ = db.QueryRow(tableExistsSQLite, table).Scan(&exists)
	} else if dialect == Postgres {
		_ = db.QueryRow(tableExistsPostgres, table).Scan(&exists)
	}
	return
}

const createOwnerTable = `
CREATE TABLE IF NOT EXISTS database_owner (
	key   INTEGER PRIMARY KEY DEFAULT 0,
	owner TEXT NOT NULL
)
`

func CheckDatabaseOwner(dialect Dialect, db *sql.DB) error {
	var owner string
	if !IgnoreForeignTables {
		if tableExists(dialect, db, "state_groups_state") {
			return fmt.Errorf("%w (found state_groups_state, likely belonging to Synapse)", ErrForeignTables)
		} else if tableExists(dialect, db, "goose_db_version") {
			return fmt.Errorf("%w (found goose_db_version, possibly belonging to Dendrite)", ErrForeignTables)
		}
	}
	if _, err := db.Exec(createOwnerTable); err != nil {
		return fmt.Errorf("failed to ensure database owner table exists: %w", err)
	} else if err = db.QueryRow("SELECT owner FROM database_owner WHERE key=0").Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec("INSERT INTO database_owner (owner) VALUES ($1)", databaseOwner)
		if err != nil {
			return fmt.Errorf("failed to insert database owner: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check database owner: %w", err)
	} else if owner != databaseOwner {
		return fmt.Errorf("%w %s", ErrNotOwned, owner)
	}
	return nil
}

func SetVersion(tx *sql.Tx, version int) error {
	_, err := tx.Exec("DELETE FROM version")
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO version (version) VALUES ($1)", version)
	return err
}

func execMany(tx *sql.Tx, queries ...string) error {
	for _, query := range queries {
		_, err := tx.Exec(query)
		if err != nil {
			return err
		}
	}
	return nil
}

func Run(log log.Logger, dialectName string, db *sql.DB) error {
	var dialect Dialect
	switch strings.ToLower(dialectName) {
	case "postgres":
		dialect = Postgres
	case "sqlite3":
		dialect = SQLite
	default:
		return fmt.Errorf("unknown dialect %s", dialectName)
	}

	err := CheckDatabaseOwner(dialect, db)
	if err != nil {
		return err
	}

	version, err := GetVersion(db)
	if err != nil {
		return err
	}

	if version > NumberOfUpgrades {
		return fmt.Errorf("%w: currently on v%d, latest known: v%d", ErrUnsupportedDatabaseVersion, version, NumberOfUpgrades)
	}

	log.Infofln("Database currently on v%d, latest: v%d", version, NumberOfUpgrades)
	for i, upgradeItem := range upgrades[version:] {
		if upgradeItem.fn == nil {
			continue
		}
		log.Infofln("Upgrading database to v%d: %s", version+i+1, upgradeItem.message)
		var tx *sql.Tx
		tx, err = db.Begin()
		if err != nil {
			return err
		}
		err = upgradeItem.fn(tx, context{dialect, db, log})
		if err != nil {
			return err
		}
		err = SetVersion(tx, version+i+1)
		if err != nil {
			return err
		}
		err = tx.Commit()
		if err != nil {
			return err
		}
	}
	return nil
}
