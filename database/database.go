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

package database

import (
	"errors"
	"net"
	"time"

	"github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"

	"maunium.net/go/mautrix-whatsapp/database/upgrades"
	"maunium.net/go/mautrix/util/dbutil"
)

func init() {
	sqlstore.PostgresArrayWrapper = pq.Array
}

type Database struct {
	*dbutil.Database

	User     *UserQuery
	Portal   *PortalQuery
	Puppet   *PuppetQuery
	Message  *MessageQuery
	Reaction *ReactionQuery

	DisappearingMessage  *DisappearingMessageQuery
	Backfill             *BackfillQuery
	HistorySync          *HistorySyncQuery
	MediaBackfillRequest *MediaBackfillRequestQuery
}

func New(baseDB *dbutil.Database) *Database {
	db := &Database{Database: baseDB}
	db.UpgradeTable = upgrades.Table
	db.User = &UserQuery{
		db:  db,
		log: db.Log.Sub("User"),
	}
	db.Portal = &PortalQuery{
		db:  db,
		log: db.Log.Sub("Portal"),
	}
	db.Puppet = &PuppetQuery{
		db:  db,
		log: db.Log.Sub("Puppet"),
	}
	db.Message = &MessageQuery{
		db:  db,
		log: db.Log.Sub("Message"),
	}
	db.Reaction = &ReactionQuery{
		db:  db,
		log: db.Log.Sub("Reaction"),
	}
	db.DisappearingMessage = &DisappearingMessageQuery{
		db:  db,
		log: db.Log.Sub("DisappearingMessage"),
	}
	db.Backfill = &BackfillQuery{
		db:  db,
		log: db.Log.Sub("Backfill"),
	}
	db.HistorySync = &HistorySyncQuery{
		db:  db,
		log: db.Log.Sub("HistorySync"),
	}
	db.MediaBackfillRequest = &MediaBackfillRequestQuery{
		db:  db,
		log: db.Log.Sub("MediaBackfillRequest"),
	}
	return db
}

func isRetryableError(err error) bool {
	if pqError := (&pq.Error{}); errors.As(err, &pqError) {
		switch pqError.Code.Class() {
		case "08", // Connection Exception
			"53", // Insufficient Resources (e.g. too many connections)
			"57": // Operator Intervention (e.g. server restart)
			return true
		}
	} else if netError := (&net.OpError{}); errors.As(err, &netError) {
		return true
	}
	return false
}

func (db *Database) HandleSignalStoreError(device *store.Device, action string, attemptIndex int, err error) (retry bool) {
	if db.Dialect != dbutil.SQLite && isRetryableError(err) {
		sleepTime := time.Duration(attemptIndex*2) * time.Second
		device.Log.Warnf("Failed to %s (attempt #%d): %v - retrying in %v", action, attemptIndex+1, err, sleepTime)
		time.Sleep(sleepTime)
		return true
	}
	device.Log.Errorf("Failed to %s: %v", action, err)
	return false
}
