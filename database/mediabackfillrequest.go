// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan, Sumner Evans
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

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	log "maunium.net/go/maulogger/v2"

	"maunium.net/go/mautrix/id"
)

type MediaBackfillRequestStatus int

const (
	MediaBackfillRequestStatusNotRequested MediaBackfillRequestStatus = iota
	MediaBackfillRequestStatusRequested
	MediaBackfillRequestStatusRequestFailed
)

type MediaBackfillRequestQuery struct {
	db  *Database
	log log.Logger
}

type MediaBackfillRequest struct {
	db  *Database
	log log.Logger

	UserID    id.UserID
	PortalKey *PortalKey
	EventID   id.EventID
	MediaKey  []byte
	Status    MediaBackfillRequestStatus
	Error     string
}

func (mbrq *MediaBackfillRequestQuery) newMediaBackfillRequest() *MediaBackfillRequest {
	return &MediaBackfillRequest{
		db:        mbrq.db,
		log:       mbrq.log,
		PortalKey: &PortalKey{},
	}
}

func (mbrq *MediaBackfillRequestQuery) NewMediaBackfillRequestWithValues(userID id.UserID, portalKey *PortalKey, eventID id.EventID, mediaKey []byte) *MediaBackfillRequest {
	return &MediaBackfillRequest{
		db:        mbrq.db,
		log:       mbrq.log,
		UserID:    userID,
		PortalKey: portalKey,
		EventID:   eventID,
		MediaKey:  mediaKey,
		Status:    MediaBackfillRequestStatusNotRequested,
	}
}

const (
	getMediaBackfillRequestsForUser = `
		SELECT user_mxid, portal_jid, portal_receiver, event_id, media_key, status, error
		FROM media_backfill_requests
		WHERE user_mxid=$1
			AND status=0
	`
)

func (mbr *MediaBackfillRequest) Upsert() {
	_, err := mbr.db.Exec(`
		INSERT INTO media_backfill_requests (user_mxid, portal_jid, portal_receiver, event_id, media_key, status, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_mxid, portal_jid, portal_receiver, event_id)
		DO UPDATE SET
			media_key=EXCLUDED.media_key,
			status=EXCLUDED.status,
			error=EXCLUDED.error`,
		mbr.UserID,
		mbr.PortalKey.JID.String(),
		mbr.PortalKey.Receiver.String(),
		mbr.EventID,
		mbr.MediaKey,
		mbr.Status,
		mbr.Error)
	if err != nil {
		mbr.log.Warnfln("Failed to insert media backfill request %s/%s/%s: %v", mbr.UserID, mbr.PortalKey.String(), mbr.EventID, err)
	}
}

func (mbr *MediaBackfillRequest) Scan(row dbutil.Scannable) *MediaBackfillRequest {
	err := row.Scan(&mbr.UserID, &mbr.PortalKey.JID, &mbr.PortalKey.Receiver, &mbr.EventID, &mbr.MediaKey, &mbr.Status, &mbr.Error)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			mbr.log.Errorln("Database scan failed:", err)
		}
		return nil
	}
	return mbr
}

func (mbrq *MediaBackfillRequestQuery) GetMediaBackfillRequestsForUser(userID id.UserID) (requests []*MediaBackfillRequest) {
	rows, err := mbrq.db.Query(getMediaBackfillRequestsForUser, userID)
	defer rows.Close()
	if err != nil || rows == nil {
		return nil
	}
	for rows.Next() {
		requests = append(requests, mbrq.newMediaBackfillRequest().Scan(rows))
	}
	return
}

func (mbrq *MediaBackfillRequestQuery) DeleteAllMediaBackfillRequests(userID id.UserID) {
	_, err := mbrq.db.Exec("DELETE FROM media_backfill_requests WHERE user_mxid=$1", userID)
	if err != nil {
		mbrq.log.Warnfln("Failed to delete media backfill requests for %s: %v", userID, err)
	}
}
