-- v10 (compatible with v3+): Add MatrixRTC/LiveKit call metadata
CREATE TABLE whatsapp_matrixrtc_call (
    bridge_id               TEXT   NOT NULL,
    user_login_id           TEXT   NOT NULL,
    wa_call_id              TEXT   NOT NULL,
    room_id                 TEXT   NOT NULL,
    portal_id               TEXT   NOT NULL,
    portal_receiver         TEXT   NOT NULL,
    peer_jid                TEXT   NOT NULL,
    direction               TEXT   NOT NULL,
    media_kind              TEXT   NOT NULL,
    focus_type              TEXT   NOT NULL,
    livekit_service_url     TEXT   NOT NULL,
    livekit_room            TEXT,
    matrix_participant_mxid TEXT,
    matrix_session_id       TEXT,
    selected_publisher_id   TEXT,
    audio_policy            TEXT   NOT NULL,
    state                   TEXT   NOT NULL,
    created_ts              BIGINT NOT NULL,
    joined_ts               BIGINT,
    answered_ts             BIGINT,
    ended_ts                BIGINT,
    end_reason              TEXT,
    last_error              TEXT,

    PRIMARY KEY (bridge_id, user_login_id, wa_call_id),
    CONSTRAINT whatsapp_matrixrtc_call_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login (bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT whatsapp_matrixrtc_call_portal_fkey FOREIGN KEY (bridge_id, portal_id, portal_receiver)
        REFERENCES portal (bridge_id, id, receiver) ON UPDATE CASCADE ON DELETE CASCADE
);
CREATE INDEX whatsapp_matrixrtc_call_room_idx ON whatsapp_matrixrtc_call (bridge_id, room_id, state);
