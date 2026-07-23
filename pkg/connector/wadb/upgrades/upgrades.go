package upgrades

import (
	"embed"

	"go.mau.fi/util/dbutil"
)

//go:embed *.sql
var rawUpgrades embed.FS

var Table = dbutil.BuildUpgradeTable().
	WithFS(rawUpgrades).
	Finish()
