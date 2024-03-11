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

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

type BackfillStateQuery struct {
	*dbutil.QueryHelper[*BackfillState]
}

func newBackfillState(qh *dbutil.QueryHelper[*BackfillState]) *BackfillState {
	return &BackfillState{qh: qh}
}

func (bq *BackfillStateQuery) NewBackfillState(userID id.UserID, portalKey PortalKey) *BackfillState {
	return &BackfillState{
		qh: bq.QueryHelper,

		UserID: userID,
		Portal: portalKey,
	}
}

const (
	getBackfillStateQuery = `
		SELECT user_mxid, portal_jid, portal_receiver, processing_batch, backfill_complete, first_expected_ts
		FROM backfill_state
		WHERE user_mxid=$1
			AND portal_jid=$2
			AND portal_receiver=$3
	`
	upsertBackfillStateQuery = `
		INSERT INTO backfill_state
			(user_mxid, portal_jid, portal_receiver, processing_batch, backfill_complete, first_expected_ts)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_mxid, portal_jid, portal_receiver)
		DO UPDATE SET
			processing_batch=EXCLUDED.processing_batch,
			backfill_complete=EXCLUDED.backfill_complete,
			first_expected_ts=EXCLUDED.first_expected_ts
	`
)

func (bq *BackfillStateQuery) GetBackfillState(ctx context.Context, userID id.UserID, portalKey PortalKey) (*BackfillState, error) {
	return bq.QueryOne(ctx, getBackfillStateQuery, userID, portalKey.JID, portalKey.Receiver)
}

type BackfillState struct {
	qh *dbutil.QueryHelper[*BackfillState]

	UserID                 id.UserID
	Portal                 PortalKey
	ProcessingBatch        bool
	BackfillComplete       bool
	FirstExpectedTimestamp uint64
}

func (b *BackfillState) Scan(row dbutil.Scannable) (*BackfillState, error) {
	return dbutil.ValueOrErr(b, row.Scan(
		&b.UserID, &b.Portal.JID, &b.Portal.Receiver, &b.ProcessingBatch, &b.BackfillComplete, &b.FirstExpectedTimestamp,
	))
}

func (b *BackfillState) sqlVariables() []any {
	return []any{b.UserID, b.Portal.JID, b.Portal.Receiver, b.ProcessingBatch, b.BackfillComplete, b.FirstExpectedTimestamp}
}

func (b *BackfillState) Upsert(ctx context.Context) error {
	return b.qh.Exec(ctx, upsertBackfillStateQuery, b.sqlVariables()...)
}

func (b *BackfillState) SetProcessingBatch(ctx context.Context, processing bool) error {
	b.ProcessingBatch = processing
	return b.Upsert(ctx)
}
