// Copyright (c) 2022 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package upgrades

import (
	"database/sql"
	"embed"
	"errors"

	"maunium.net/go/mautrix/util/dbutil"
)

var Table dbutil.UpgradeTable

//go:embed *.sql
var rawUpgrades embed.FS

func init() {
	Table.Register(-1, 43, "Unsupported version", func(tx *sql.Tx, database *dbutil.Database) error {
		return errors.New("please upgrade to mautrix-whatsapp v0.4.0 before upgrading to a newer version")
	})
	Table.RegisterFS(rawUpgrades)
}
