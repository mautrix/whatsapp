// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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
	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"

	"maunium.net/go/mautrix-whatsapp/database/upgrades"
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
	BackfillQueue        *BackfillTaskQuery
	BackfillState        *BackfillStateQuery
	HistorySync          *HistorySyncQuery
	MediaBackfillRequest *MediaBackfillRequestQuery
}

func New(db *dbutil.Database) *Database {
	db.UpgradeTable = upgrades.Table
	return &Database{
		Database: db,
		User:     &UserQuery{dbutil.MakeQueryHelper(db, newUser)},
		Portal:   &PortalQuery{dbutil.MakeQueryHelper(db, newPortal)},
		Puppet:   &PuppetQuery{dbutil.MakeQueryHelper(db, newPuppet)},
		Message:  &MessageQuery{dbutil.MakeQueryHelper(db, newMessage)},
		Reaction: &ReactionQuery{dbutil.MakeQueryHelper(db, newReaction)},

		DisappearingMessage:  &DisappearingMessageQuery{dbutil.MakeQueryHelper(db, newDisappearingMessage)},
		BackfillQueue:        &BackfillTaskQuery{dbutil.MakeQueryHelper(db, newBackfillTask)},
		BackfillState:        &BackfillStateQuery{dbutil.MakeQueryHelper(db, newBackfillState)},
		HistorySync:          &HistorySyncQuery{dbutil.MakeQueryHelper(db, newHistorySyncConversation)},
		MediaBackfillRequest: &MediaBackfillRequestQuery{dbutil.MakeQueryHelper(db, newMediaBackfillRequest)},
	}
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
