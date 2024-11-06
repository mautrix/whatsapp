-- v0 -> v4 (compatible with v3+): Latest revision

CREATE TABLE whatsapp_poll_option_id (
    bridge_id TEXT  NOT NULL,
    msg_mxid  TEXT  NOT NULL,
    opt_id    TEXT  NOT NULL,
    opt_hash  bytea NOT NULL CHECK ( length(opt_hash) = 32 ),

    PRIMARY KEY (bridge_id, msg_mxid, opt_id),
    CONSTRAINT whatsapp_poll_option_unique_hash UNIQUE (bridge_id, msg_mxid, opt_hash),
    CONSTRAINT message_mxid_fkey FOREIGN KEY (bridge_id, msg_mxid)
        REFERENCES message (bridge_id, mxid) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE whatsapp_history_sync_conversation (
    bridge_id                    TEXT    NOT NULL,
    user_login_id                TEXT    NOT NULL,
    chat_jid                     TEXT    NOT NULL,

    last_message_timestamp       BIGINT,
    archived                     BOOLEAN,
    pinned                       BOOLEAN,
    mute_end_time                BIGINT,
    end_of_history_transfer_type INTEGER,
    ephemeral_expiration         INTEGER,
    ephemeral_setting_timestamp  BIGINT,
    marked_as_unread             BOOLEAN,
    unread_count                 INTEGER,

    bridged                      BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (bridge_id, user_login_id, chat_jid),
    CONSTRAINT whatsapp_history_sync_conversation_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login (bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE whatsapp_history_sync_message (
    bridge_id     TEXT   NOT NULL,
    user_login_id TEXT   NOT NULL,
    chat_jid      TEXT   NOT NULL,
    sender_jid    TEXT   NOT NULL,
    message_id    TEXT   NOT NULL,
    timestamp     BIGINT NOT NULL,
    data          bytea  NOT NULL,
    inserted_time BIGINT NOT NULL,

    PRIMARY KEY (bridge_id, user_login_id, chat_jid, sender_jid, message_id),
    CONSTRAINT whatsapp_history_sync_message_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login (bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT whatsapp_history_sync_message_conversation_fkey FOREIGN KEY (bridge_id, user_login_id, chat_jid)
        REFERENCES whatsapp_history_sync_conversation (bridge_id, user_login_id, chat_jid) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE whatsapp_media_backfill_request (
    bridge_id       TEXT    NOT NULL,
    user_login_id   TEXT    NOT NULL,
    message_id      TEXT    NOT NULL,
    _part_id        TEXT    NOT NULL DEFAULT '',

    portal_id       TEXT    NOT NULL,
    portal_receiver TEXT    NOT NULL,

    media_key       bytea,
    status          INTEGER NOT NULL,
    error           TEXT    NOT NULL,

    PRIMARY KEY (bridge_id, user_login_id, message_id),
    CONSTRAINT whatsapp_media_backfill_request_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login (bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT whatsapp_media_backfill_request_portal_fkey FOREIGN KEY (bridge_id, portal_id, portal_receiver)
        REFERENCES portal (bridge_id, id, receiver) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT whatsapp_media_backfill_request_message_fkey FOREIGN KEY (bridge_id, portal_receiver, message_id, _part_id)
        REFERENCES message (bridge_id, room_receiver, id, part_id) ON UPDATE CASCADE ON DELETE CASCADE
);
CREATE INDEX whatsapp_media_backfill_request_portal_idx ON whatsapp_media_backfill_request (bridge_id, portal_id, portal_receiver);
CREATE INDEX whatsapp_media_backfill_request_message_idx ON whatsapp_media_backfill_request (bridge_id, portal_receiver, message_id, _part_id);
