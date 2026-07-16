package wadb

import (
	"context"
	"database/sql"
	"time"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

type MatrixRTCCallQuery struct {
	BridgeID networkid.BridgeID
	*dbutil.QueryHelper[*MatrixRTCCall]
}

type MatrixRTCCall struct {
	BridgeID              networkid.BridgeID
	UserLoginID           networkid.UserLoginID
	WACallID              string
	RoomID                id.RoomID
	PortalKey             networkid.PortalKey
	PeerJID               types.JID
	Direction             string
	MediaKind             string
	FocusType             string
	LiveKitServiceURL     string
	LiveKitRoom           string
	MatrixParticipantMXID id.UserID
	MatrixSessionID       string
	SelectedPublisherID   string
	AudioPolicy           string
	State                 string
	CreatedTS             time.Time
	JoinedTS              time.Time
	AnsweredTS            time.Time
	EndedTS               time.Time
	EndReason             string
	LastError             string
}

const (
	upsertMatrixRTCCallQuery = `
		INSERT INTO whatsapp_matrixrtc_call (
			bridge_id, user_login_id, wa_call_id, room_id, portal_id, portal_receiver, peer_jid,
			direction, media_kind, focus_type, livekit_service_url, livekit_room,
			matrix_participant_mxid, matrix_session_id, selected_publisher_id,
			audio_policy, state, created_ts, joined_ts, answered_ts, ended_ts,
			end_reason, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (bridge_id, user_login_id, wa_call_id) DO UPDATE SET
			room_id=excluded.room_id,
			portal_id=excluded.portal_id,
			portal_receiver=excluded.portal_receiver,
			peer_jid=excluded.peer_jid,
			direction=excluded.direction,
			media_kind=excluded.media_kind,
			focus_type=excluded.focus_type,
			livekit_service_url=excluded.livekit_service_url,
			livekit_room=excluded.livekit_room,
			matrix_participant_mxid=excluded.matrix_participant_mxid,
			matrix_session_id=excluded.matrix_session_id,
			selected_publisher_id=excluded.selected_publisher_id,
			audio_policy=excluded.audio_policy,
			state=excluded.state,
			joined_ts=excluded.joined_ts,
			answered_ts=excluded.answered_ts,
			ended_ts=excluded.ended_ts,
			end_reason=excluded.end_reason,
			last_error=excluded.last_error
	`
	getMatrixRTCCallQuery = `
		SELECT
			bridge_id, user_login_id, wa_call_id, room_id, portal_id, portal_receiver, peer_jid,
			direction, media_kind, focus_type, livekit_service_url, livekit_room,
			matrix_participant_mxid, matrix_session_id, selected_publisher_id,
			audio_policy, state, created_ts, joined_ts, answered_ts, ended_ts,
			end_reason, last_error
		FROM whatsapp_matrixrtc_call
		WHERE bridge_id=$1 AND user_login_id=$2 AND wa_call_id=$3
	`
	getActiveMatrixRTCCallsForLoginQuery = `
		SELECT
			bridge_id, user_login_id, wa_call_id, room_id, portal_id, portal_receiver, peer_jid,
			direction, media_kind, focus_type, livekit_service_url, livekit_room,
			matrix_participant_mxid, matrix_session_id, selected_publisher_id,
			audio_policy, state, created_ts, joined_ts, answered_ts, ended_ts,
			end_reason, last_error
		FROM whatsapp_matrixrtc_call
		WHERE bridge_id=$1 AND user_login_id=$2 AND ended_ts IS NULL
	`
	getActiveMatrixRTCCallsInRoomQuery = `
		SELECT
			bridge_id, user_login_id, wa_call_id, room_id, portal_id, portal_receiver, peer_jid,
			direction, media_kind, focus_type, livekit_service_url, livekit_room,
			matrix_participant_mxid, matrix_session_id, selected_publisher_id,
			audio_policy, state, created_ts, joined_ts, answered_ts, ended_ts,
			end_reason, last_error
		FROM whatsapp_matrixrtc_call
		WHERE bridge_id=$1 AND room_id=$2 AND ended_ts IS NULL
	`
	markMatrixRTCCallEndedQuery = `
		UPDATE whatsapp_matrixrtc_call
		SET state=$4, ended_ts=$5, end_reason=$6, last_error=$7
		WHERE bridge_id=$1 AND user_login_id=$2 AND wa_call_id=$3
	`
	deleteMatrixRTCCallQuery = `
		DELETE FROM whatsapp_matrixrtc_call
		WHERE bridge_id=$1 AND user_login_id=$2 AND wa_call_id=$3
	`
)

func (cq *MatrixRTCCallQuery) Put(ctx context.Context, call *MatrixRTCCall) error {
	call.BridgeID = cq.BridgeID
	return cq.Exec(ctx, upsertMatrixRTCCallQuery, call.sqlVariables()...)
}

func (cq *MatrixRTCCallQuery) Get(ctx context.Context, loginID networkid.UserLoginID, waCallID string) (*MatrixRTCCall, error) {
	return cq.QueryOne(ctx, getMatrixRTCCallQuery, cq.BridgeID, loginID, waCallID)
}

func (cq *MatrixRTCCallQuery) GetActiveForLogin(ctx context.Context, loginID networkid.UserLoginID) ([]*MatrixRTCCall, error) {
	return cq.QueryMany(ctx, getActiveMatrixRTCCallsForLoginQuery, cq.BridgeID, loginID)
}

func (cq *MatrixRTCCallQuery) GetActiveInRoom(ctx context.Context, roomID id.RoomID) ([]*MatrixRTCCall, error) {
	return cq.QueryMany(ctx, getActiveMatrixRTCCallsInRoomQuery, cq.BridgeID, roomID)
}

func (cq *MatrixRTCCallQuery) MarkEnded(ctx context.Context, loginID networkid.UserLoginID, waCallID, state, reason, lastError string, ended time.Time) error {
	return cq.Exec(ctx, markMatrixRTCCallEndedQuery, cq.BridgeID, loginID, waCallID, state, nullableUnix(ended), reason, lastError)
}

func (cq *MatrixRTCCallQuery) Delete(ctx context.Context, loginID networkid.UserLoginID, waCallID string) error {
	return cq.Exec(ctx, deleteMatrixRTCCallQuery, cq.BridgeID, loginID, waCallID)
}

func (call *MatrixRTCCall) Scan(row dbutil.Scannable) (*MatrixRTCCall, error) {
	var liveKitRoom, participantMXID, matrixSessionID, selectedPublisherID, endReason, lastError sql.NullString
	var joinedTS, answeredTS, endedTS sql.NullInt64
	var createdTS int64
	err := row.Scan(
		&call.BridgeID,
		&call.UserLoginID,
		&call.WACallID,
		&call.RoomID,
		&call.PortalKey.ID,
		&call.PortalKey.Receiver,
		&call.PeerJID,
		&call.Direction,
		&call.MediaKind,
		&call.FocusType,
		&call.LiveKitServiceURL,
		&liveKitRoom,
		&participantMXID,
		&matrixSessionID,
		&selectedPublisherID,
		&call.AudioPolicy,
		&call.State,
		&createdTS,
		&joinedTS,
		&answeredTS,
		&endedTS,
		&endReason,
		&lastError,
	)
	if err != nil {
		return nil, err
	}
	call.CreatedTS = unixToTime(createdTS)
	call.JoinedTS = nullUnixToTime(joinedTS)
	call.AnsweredTS = nullUnixToTime(answeredTS)
	call.EndedTS = nullUnixToTime(endedTS)
	call.LiveKitRoom = liveKitRoom.String
	call.MatrixParticipantMXID = id.UserID(participantMXID.String)
	call.MatrixSessionID = matrixSessionID.String
	call.SelectedPublisherID = selectedPublisherID.String
	call.EndReason = endReason.String
	call.LastError = lastError.String
	return call, nil
}

func (call *MatrixRTCCall) sqlVariables() []any {
	return []any{
		call.BridgeID,
		call.UserLoginID,
		call.WACallID,
		call.RoomID,
		call.PortalKey.ID,
		call.PortalKey.Receiver,
		call.PeerJID,
		call.Direction,
		call.MediaKind,
		call.FocusType,
		call.LiveKitServiceURL,
		nullString(call.LiveKitRoom),
		nullString(string(call.MatrixParticipantMXID)),
		nullString(call.MatrixSessionID),
		nullString(call.SelectedPublisherID),
		call.AudioPolicy,
		call.State,
		nullableUnix(call.CreatedTS),
		nullableUnix(call.JoinedTS),
		nullableUnix(call.AnsweredTS),
		nullableUnix(call.EndedTS),
		nullString(call.EndReason),
		nullString(call.LastError),
	}
}

func nullString(str string) *string {
	if str == "" {
		return nil
	}
	return &str
}

func nullableUnix(ts time.Time) *int64 {
	if ts.IsZero() {
		return nil
	}
	unix := ts.Unix()
	return &unix
}

func unixToTime(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func nullUnixToTime(ts sql.NullInt64) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return unixToTime(ts.Int64)
}
