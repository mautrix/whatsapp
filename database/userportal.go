// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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
	"database/sql"
	"errors"
	"time"
)

func (user *User) GetLastReadTS(portal PortalKey) time.Time {
	user.lastReadCacheLock.Lock()
	defer user.lastReadCacheLock.Unlock()
	if cached, ok := user.lastReadCache[portal]; ok {
		return cached
	}
	var ts int64
	err := user.db.QueryRow("SELECT last_read_ts FROM user_portal WHERE user_mxid=$1 AND portal_jid=$2 AND portal_receiver=$3", user.MXID, portal.JID, portal.Receiver).Scan(&ts)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		user.log.Warnfln("Failed to scan last read timestamp from user portal table: %v", err)
	}
	if ts == 0 {
		user.lastReadCache[portal] = time.Time{}
	} else {
		user.lastReadCache[portal] = time.Unix(ts, 0)
	}
	return user.lastReadCache[portal]
}

func (user *User) SetLastReadTS(portal PortalKey, ts time.Time) {
	user.lastReadCacheLock.Lock()
	defer user.lastReadCacheLock.Unlock()
	_, err := user.db.Exec(`
			INSERT INTO user_portal (user_mxid, portal_jid, portal_receiver, last_read_ts) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_mxid, portal_jid, portal_receiver) DO UPDATE SET last_read_ts=excluded.last_read_ts WHERE user_portal.last_read_ts<excluded.last_read_ts
		`, user.MXID, portal.JID, portal.Receiver, ts.Unix())
	if err != nil {
		user.log.Warnfln("Failed to update last read timestamp: %v", err)
	} else {
		user.lastReadCache[portal] = ts
	}
}

func (user *User) IsInSpace(portal PortalKey) bool {
	user.inSpaceCacheLock.Lock()
	defer user.inSpaceCacheLock.Unlock()
	if cached, ok := user.inSpaceCache[portal]; ok {
		return cached
	}
	var inSpace bool
	err := user.db.QueryRow("SELECT in_space FROM user_portal WHERE user_mxid=$1 AND portal_jid=$2 AND portal_receiver=$3", user.MXID, portal.JID, portal.Receiver).Scan(&inSpace)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		user.log.Warnfln("Failed to scan in space status from user portal table: %v", err)
	}
	user.inSpaceCache[portal] = inSpace
	return inSpace
}

func (user *User) MarkInSpace(portal PortalKey) {
	user.inSpaceCacheLock.Lock()
	defer user.inSpaceCacheLock.Unlock()
	_, err := user.db.Exec(`
			INSERT INTO user_portal (user_mxid, portal_jid, portal_receiver, in_space) VALUES ($1, $2, $3, true)
			ON CONFLICT (user_mxid, portal_jid, portal_receiver) DO UPDATE SET in_space=true
		`, user.MXID, portal.JID, portal.Receiver)
	if err != nil {
		user.log.Warnfln("Failed to update in space status: %v", err)
	} else {
		user.inSpaceCache[portal] = true
	}
}
