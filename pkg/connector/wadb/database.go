package wadb

import (
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"maunium.net/go/mautrix-whatsapp/pkg/connector/wadb/upgrades"
)

type Database struct {
	*dbutil.Database
}

func New(db *dbutil.Database, log zerolog.Logger) *Database {
	db = db.Child("whatsapp_version", upgrades.Table, dbutil.ZeroLogger(log))
	return &Database{
		Database: db,
	}
}
