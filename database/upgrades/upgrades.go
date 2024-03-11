// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package upgrades

import (
	"context"
	"embed"
	"errors"

	"go.mau.fi/util/dbutil"
)

var Table dbutil.UpgradeTable

//go:embed *.sql
var rawUpgrades embed.FS

func init() {
	Table.Register(-1, 35, 0, "Unsupported version", false, func(ctx context.Context, database *dbutil.Database) error {
		return errors.New("please upgrade to mautrix-whatsapp v0.4.0 before upgrading to a newer version")
	})
	Table.RegisterFS(rawUpgrades)
}
