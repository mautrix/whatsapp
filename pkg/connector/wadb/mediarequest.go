package wadb

import (
	"context"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

type MediaBackfillRequestStatus int

const (
	MediaBackfillRequestStatusNotRequested  MediaBackfillRequestStatus = 0
	MediaBackfillRequestStatusRequested     MediaBackfillRequestStatus = 1
	MediaBackfillRequestStatusRequestFailed MediaBackfillRequestStatus = 2
)

type MediaRequestQuery struct {
	BridgeID networkid.BridgeID
	*dbutil.QueryHelper[*MediaRequest]
}

type MediaRequest struct {
	BridgeID    networkid.BridgeID
	UserLoginID networkid.UserLoginID
	PortalKey   networkid.PortalKey
	EventID     id.EventID
	MediaKey    []byte
	Status      MediaBackfillRequestStatus
	Error       string
}

const (
	upsertMediaRequestQuery = `
		INSERT INTO whatsapp_media_backfill_request (
			bridge_id, user_login_id, portal_id, portal_receiver, event_id, media_key, status, error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (bridge_id, user_login_id, portal_id, portal_receiver, event_id) DO UPDATE SET
			media_key=excluded.media_key, status=excluded.status, error=excluded.error
	`
	getAllUnrequestedMediaRequestsForUserLoginQuery = `
		SELECT bridge_id, user_login_id, portal_id, portal_receiver, event_id, media_key, status, error
		FROM whatsapp_media_backfill_request
		WHERE bridge_id=$1 AND user_login_id=$2 AND status=0
	`
)

func (mrq *MediaRequestQuery) Put(ctx context.Context, mr *MediaRequest) error {
	mr.BridgeID = mrq.BridgeID
	return mrq.Exec(ctx, upsertMediaRequestQuery, mr.sqlVariables()...)
}

func (mrq *MediaRequestQuery) GetUnrequestedForUserLogin(ctx context.Context, loginID networkid.UserLoginID) ([]*MediaRequest, error) {
	return mrq.QueryMany(ctx, getAllUnrequestedMediaRequestsForUserLoginQuery, mrq.BridgeID, loginID)
}

func (mr *MediaRequest) Scan(row dbutil.Scannable) (*MediaRequest, error) {
	err := row.Scan(&mr.BridgeID, &mr.UserLoginID, &mr.PortalKey.ID, &mr.PortalKey.Receiver, &mr.EventID, &mr.MediaKey, &mr.Status, &mr.Error)
	if err != nil {
		return nil, err
	}
	return mr, nil
}

func (mr *MediaRequest) sqlVariables() []any {
	return []any{mr.BridgeID, mr.UserLoginID, mr.PortalKey.ID, mr.PortalKey.Receiver, mr.EventID, mr.MediaKey, mr.Status, mr.Error}
}
