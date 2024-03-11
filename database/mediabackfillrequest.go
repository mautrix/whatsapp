// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan, Sumner Evans
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

	_ "github.com/mattn/go-sqlite3"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/util/dbutil"
)

type MediaBackfillRequestStatus int

const (
	MediaBackfillRequestStatusNotRequested MediaBackfillRequestStatus = iota
	MediaBackfillRequestStatusRequested
	MediaBackfillRequestStatusRequestFailed
)

type MediaBackfillRequestQuery struct {
	*dbutil.QueryHelper[*MediaBackfillRequest]
}

const (
	getAllMediaBackfillRequestsForUserQuery = `
		SELECT user_mxid, portal_jid, portal_receiver, event_id, media_key, status, error
		FROM media_backfill_requests
		WHERE user_mxid=$1
			AND status=0
	`
	deleteAllMediaBackfillRequestsForUserQuery = "DELETE FROM media_backfill_requests WHERE user_mxid=$1"
	upsertMediaBackfillRequestQuery            = `
		INSERT INTO media_backfill_requests (user_mxid, portal_jid, portal_receiver, event_id, media_key, status, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_mxid, portal_jid, portal_receiver, event_id)
		DO UPDATE SET
			media_key=excluded.media_key,
			status=excluded.status,
			error=excluded.error
	`
)

func (mbrq *MediaBackfillRequestQuery) GetMediaBackfillRequestsForUser(ctx context.Context, userID id.UserID) ([]*MediaBackfillRequest, error) {
	return mbrq.QueryMany(ctx, getAllMediaBackfillRequestsForUserQuery, userID)
}

func (mbrq *MediaBackfillRequestQuery) DeleteAllMediaBackfillRequests(ctx context.Context, userID id.UserID) error {
	return mbrq.Exec(ctx, deleteAllMediaBackfillRequestsForUserQuery, userID)
}

func newMediaBackfillRequest(qh *dbutil.QueryHelper[*MediaBackfillRequest]) *MediaBackfillRequest {
	return &MediaBackfillRequest{
		qh: qh,
	}
}

func (mbrq *MediaBackfillRequestQuery) NewMediaBackfillRequestWithValues(userID id.UserID, portalKey PortalKey, eventID id.EventID, mediaKey []byte) *MediaBackfillRequest {
	return &MediaBackfillRequest{
		qh: mbrq.QueryHelper,

		UserID:    userID,
		PortalKey: portalKey,
		EventID:   eventID,
		MediaKey:  mediaKey,
		Status:    MediaBackfillRequestStatusNotRequested,
	}
}

type MediaBackfillRequest struct {
	qh *dbutil.QueryHelper[*MediaBackfillRequest]

	UserID    id.UserID
	PortalKey PortalKey
	EventID   id.EventID
	MediaKey  []byte
	Status    MediaBackfillRequestStatus
	Error     string
}

func (mbr *MediaBackfillRequest) Scan(row dbutil.Scannable) (*MediaBackfillRequest, error) {
	return dbutil.ValueOrErr(mbr, row.Scan(&mbr.UserID, &mbr.PortalKey.JID, &mbr.PortalKey.Receiver, &mbr.EventID, &mbr.MediaKey, &mbr.Status, &mbr.Error))
}

func (mbr *MediaBackfillRequest) sqlVariables() []any {
	return []any{mbr.UserID, mbr.PortalKey.JID, mbr.PortalKey.Receiver, mbr.EventID, mbr.MediaKey, mbr.Status, mbr.Error}
}

func (mbr *MediaBackfillRequest) Upsert(ctx context.Context) error {
	return mbr.qh.Exec(ctx, upsertMediaBackfillRequestQuery, mbr.sqlVariables()...)
}
