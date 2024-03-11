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
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rs/zerolog"
)

const (
	getLastReadTSQuery = "SELECT last_read_ts FROM user_portal WHERE user_mxid=$1 AND portal_jid=$2 AND portal_receiver=$3"
	setLastReadTSQuery = `
			INSERT INTO user_portal (user_mxid, portal_jid, portal_receiver, last_read_ts) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_mxid, portal_jid, portal_receiver) DO UPDATE SET last_read_ts=excluded.last_read_ts WHERE user_portal.last_read_ts<excluded.last_read_ts
		`
	getIsInSpaceQuery = "SELECT in_space FROM user_portal WHERE user_mxid=$1 AND portal_jid=$2 AND portal_receiver=$3"
	setIsInSpaceQuery = `
			INSERT INTO user_portal (user_mxid, portal_jid, portal_receiver, in_space) VALUES ($1, $2, $3, true)
			ON CONFLICT (user_mxid, portal_jid, portal_receiver) DO UPDATE SET in_space=true
		`
)

func (user *User) GetLastReadTS(ctx context.Context, portal PortalKey) time.Time {
	user.lastReadCacheLock.Lock()
	defer user.lastReadCacheLock.Unlock()
	if cached, ok := user.lastReadCache[portal]; ok {
		return cached
	}
	var ts int64
	var parsedTS time.Time
	err := user.qh.GetDB().QueryRow(ctx, getLastReadTSQuery, user.MXID, portal.JID, portal.Receiver).Scan(&ts)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		zerolog.Ctx(ctx).Err(err).
			Str("user_id", user.MXID.String()).
			Any("portal_key", portal).
			Msg("Failed to query last read timestamp")
		return parsedTS
	}
	if ts != 0 {
		parsedTS = time.Unix(ts, 0)
	}
	user.lastReadCache[portal] = parsedTS
	return user.lastReadCache[portal]
}

func (user *User) SetLastReadTS(ctx context.Context, portal PortalKey, ts time.Time) {
	user.lastReadCacheLock.Lock()
	defer user.lastReadCacheLock.Unlock()
	_, err := user.qh.GetDB().Exec(ctx, setLastReadTSQuery, user.MXID, portal.JID, portal.Receiver, ts.Unix())
	if err != nil {
		zerolog.Ctx(ctx).Err(err).
			Str("user_id", user.MXID.String()).
			Any("portal_key", portal).
			Msg("Failed to update last read timestamp")
	} else {
		zerolog.Ctx(ctx).Debug().
			Str("user_id", user.MXID.String()).
			Any("portal_key", portal).
			Time("last_read_ts", ts).
			Msg("Updated last read timestamp of portal")
		user.lastReadCache[portal] = ts
	}
}

func (user *User) IsInSpace(ctx context.Context, portal PortalKey) bool {
	user.inSpaceCacheLock.Lock()
	defer user.inSpaceCacheLock.Unlock()
	if cached, ok := user.inSpaceCache[portal]; ok {
		return cached
	}
	var inSpace bool
	err := user.qh.GetDB().QueryRow(ctx, getIsInSpaceQuery, user.MXID, portal.JID, portal.Receiver).Scan(&inSpace)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		zerolog.Ctx(ctx).Err(err).
			Str("user_id", user.MXID.String()).
			Any("portal_key", portal).
			Msg("Failed to query in space status")
		return false
	}
	user.inSpaceCache[portal] = inSpace
	return inSpace
}

func (user *User) MarkInSpace(ctx context.Context, portal PortalKey) {
	user.inSpaceCacheLock.Lock()
	defer user.inSpaceCacheLock.Unlock()
	_, err := user.qh.GetDB().Exec(ctx, setIsInSpaceQuery, user.MXID, portal.JID, portal.Receiver)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).
			Str("user_id", user.MXID.String()).
			Any("portal_key", portal).
			Msg("Failed to update in space status")
	} else {
		user.inSpaceCache[portal] = true
	}
}
