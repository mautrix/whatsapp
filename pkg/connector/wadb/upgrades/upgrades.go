package upgrades

import (
	"embed"

	"go.mau.fi/util/dbutil"
)

var Table dbutil.UpgradeTable

//go:embed *.sql
var rawUpgrades embed.FS

func init() {
	Table.RegisterFS(rawUpgrades)
}
