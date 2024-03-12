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
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"maunium.net/go/mautrix/id"
)

type BackfillType int

const (
	BackfillImmediate BackfillType = 0
	BackfillForward   BackfillType = 100
	BackfillDeferred  BackfillType = 200
)

func (bt BackfillType) String() string {
	switch bt {
	case BackfillImmediate:
		return "IMMEDIATE"
	case BackfillForward:
		return "FORWARD"
	case BackfillDeferred:
		return "DEFERRED"
	}
	return "UNKNOWN"
}

type BackfillTaskQuery struct {
	*dbutil.QueryHelper[*BackfillTask]

	//backfillQueryLock sync.Mutex
}

func newBackfillTask(qh *dbutil.QueryHelper[*BackfillTask]) *BackfillTask {
	return &BackfillTask{qh: qh}
}

func (bq *BackfillTaskQuery) NewWithValues(userID id.UserID, backfillType BackfillType, priority int, portal PortalKey, timeStart *time.Time, maxBatchEvents, maxTotalEvents, batchDelay int) *BackfillTask {
	return &BackfillTask{
		qh: bq.QueryHelper,

		UserID:         userID,
		BackfillType:   backfillType,
		Priority:       priority,
		Portal:         portal,
		TimeStart:      timeStart,
		MaxBatchEvents: maxBatchEvents,
		MaxTotalEvents: maxTotalEvents,
		BatchDelay:     batchDelay,
	}
}

const (
	getNextBackfillTaskQueryTemplate = `
		SELECT queue_id, user_mxid, type, priority, portal_jid, portal_receiver, time_start, max_batch_events, max_total_events, batch_delay
		FROM backfill_queue
		WHERE user_mxid=$1
			AND type IN (%s)
			AND (
				dispatch_time IS NULL
				OR (
					dispatch_time < $2
					AND completed_at IS NULL
				)
			)
		ORDER BY type, priority, queue_id
		LIMIT 1
	`
	getUnstartedOrInFlightBackfillTaskQueryTemplate = `
		SELECT 1
		FROM backfill_queue
		WHERE user_mxid=$1
			AND type IN (%s)
			AND (dispatch_time IS NULL OR completed_at IS NULL)
		LIMIT 1
	`
	deleteBackfillQueueForUserQuery   = "DELETE FROM backfill_queue WHERE user_mxid=$1"
	deleteBackfillQueueForPortalQuery = `
		DELETE FROM backfill_queue
		WHERE user_mxid=$1
			AND portal_jid=$2
			AND portal_receiver=$3
	`
	insertBackfillTaskQuery = `
		INSERT INTO backfill_queue (
			user_mxid, type, priority, portal_jid, portal_receiver, time_start,
			max_batch_events, max_total_events, batch_delay, dispatch_time, completed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING queue_id
	`
	markBackfillTaskDispatchedQuery = "UPDATE backfill_queue SET dispatch_time=$1 WHERE queue_id=$2"
	markBackfillTaskDoneQuery       = "UPDATE backfill_queue SET completed_at=$1 WHERE queue_id=$2"
)

func typesToString(backfillTypes []BackfillType) string {
	types := make([]string, len(backfillTypes))
	for i, backfillType := range backfillTypes {
		types[i] = strconv.Itoa(int(backfillType))
	}
	return strings.Join(types, ",")
}

// GetNext returns the next backfill to perform
func (bq *BackfillTaskQuery) GetNext(ctx context.Context, userID id.UserID, backfillTypes []BackfillType) (*BackfillTask, error) {
	if len(backfillTypes) == 0 {
		return nil, nil
	}
	//bq.backfillQueryLock.Lock()
	//defer bq.backfillQueryLock.Unlock()

	query := fmt.Sprintf(getNextBackfillTaskQueryTemplate, typesToString(backfillTypes))
	return bq.QueryOne(ctx, query, userID, time.Now().Add(-15*time.Minute))
}

func (bq *BackfillTaskQuery) HasUnstartedOrInFlightOfType(ctx context.Context, userID id.UserID, backfillTypes []BackfillType) (has bool) {
	if len(backfillTypes) == 0 {
		return false
	}

	//bq.backfillQueryLock.Lock()
	//defer bq.backfillQueryLock.Unlock()

	query := fmt.Sprintf(getUnstartedOrInFlightBackfillTaskQueryTemplate, typesToString(backfillTypes))
	err := bq.GetDB().QueryRow(ctx, query, userID).Scan(&has)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to check if backfill queue has jobs")
	}
	return
}

func (bq *BackfillTaskQuery) DeleteAll(ctx context.Context, userID id.UserID) error {
	//bq.backfillQueryLock.Lock()
	//defer bq.backfillQueryLock.Unlock()
	return bq.Exec(ctx, deleteBackfillQueueForUserQuery, userID)
}

func (bq *BackfillTaskQuery) DeleteAllForPortal(ctx context.Context, userID id.UserID, portalKey PortalKey) error {
	//bq.backfillQueryLock.Lock()
	//defer bq.backfillQueryLock.Unlock()
	return bq.Exec(ctx, deleteBackfillQueueForPortalQuery, userID, portalKey.JID, portalKey.Receiver)
}

type BackfillTask struct {
	qh *dbutil.QueryHelper[*BackfillTask]

	QueueID        int
	UserID         id.UserID
	BackfillType   BackfillType
	Priority       int
	Portal         PortalKey
	TimeStart      *time.Time
	MaxBatchEvents int
	MaxTotalEvents int
	BatchDelay     int
	DispatchTime   *time.Time
	CompletedAt    *time.Time
}

func (b *BackfillTask) MarshalZerologObject(evt *zerolog.Event) {
	evt.Int("queue_id", b.QueueID).
		Stringer("user_id", b.UserID).
		Stringer("backfill_type", b.BackfillType).
		Int("priority", b.Priority).
		Stringer("portal_jid", b.Portal.JID).
		Any("time_start", b.TimeStart).
		Int("max_batch_events", b.MaxBatchEvents).
		Int("max_total_events", b.MaxTotalEvents).
		Int("batch_delay", b.BatchDelay).
		Any("dispatch_time", b.DispatchTime).
		Any("completed_at", b.CompletedAt)
}

func (b *BackfillTask) String() string {
	return fmt.Sprintf(
		"BackfillTask{QueueID: %d, UserID: %s, BackfillType: %s, Priority: %d, Portal: %s, TimeStart: %s, MaxBatchEvents: %d, MaxTotalEvents: %d, BatchDelay: %d, DispatchTime: %s, CompletedAt: %s}",
		b.QueueID, b.UserID, b.BackfillType, b.Priority, b.Portal, b.TimeStart, b.MaxBatchEvents, b.MaxTotalEvents, b.BatchDelay, b.CompletedAt, b.DispatchTime,
	)
}

func (b *BackfillTask) Scan(row dbutil.Scannable) (*BackfillTask, error) {
	var maxTotalEvents, batchDelay sql.NullInt32
	err := row.Scan(
		&b.QueueID, &b.UserID, &b.BackfillType, &b.Priority, &b.Portal.JID, &b.Portal.Receiver, &b.TimeStart,
		&b.MaxBatchEvents, &maxTotalEvents, &batchDelay,
	)
	if err != nil {
		return nil, err
	}
	b.MaxTotalEvents = int(maxTotalEvents.Int32)
	b.BatchDelay = int(batchDelay.Int32)
	return b, nil
}

func (b *BackfillTask) sqlVariables() []any {
	return []any{
		b.UserID, b.BackfillType, b.Priority, b.Portal.JID, b.Portal.Receiver, b.TimeStart,
		b.MaxBatchEvents, b.MaxTotalEvents, b.BatchDelay, b.DispatchTime, b.CompletedAt,
	}
}

func (b *BackfillTask) Insert(ctx context.Context) error {
	//b.db.Backfill.backfillQueryLock.Lock()
	//defer b.db.Backfill.backfillQueryLock.Unlock()

	return b.qh.GetDB().QueryRow(ctx, insertBackfillTaskQuery, b.sqlVariables()...).Scan(&b.QueueID)
}

func (b *BackfillTask) MarkDispatched(ctx context.Context) error {
	//b.db.Backfill.backfillQueryLock.Lock()
	//defer b.db.Backfill.backfillQueryLock.Unlock()

	if b.QueueID == 0 {
		return fmt.Errorf("can't mark backfill as dispatched without queue_id")
	}
	return b.qh.Exec(ctx, markBackfillTaskDispatchedQuery, time.Now(), b.QueueID)
}

func (b *BackfillTask) MarkDone(ctx context.Context) error {
	//b.db.Backfill.backfillQueryLock.Lock()
	//defer b.db.Backfill.backfillQueryLock.Unlock()

	if b.QueueID == 0 {
		return fmt.Errorf("can't mark backfill as dispatched without queue_id")
	}
	return b.qh.Exec(ctx, markBackfillTaskDoneQuery, time.Now(), b.QueueID)
}
