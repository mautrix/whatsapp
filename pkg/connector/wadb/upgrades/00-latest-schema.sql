-- v0 -> v1 (compatible with v1+): Latest revision

CREATE TABLE whatsapp_poll_option_id (
    bridge_id TEXT,
    msg_mxid  TEXT,
    opt_id    TEXT,
    opt_hash  bytea CHECK ( length(opt_hash) = 32 ),

    PRIMARY KEY (bridge_id, msg_mxid, opt_id),
    CONSTRAINT whatsapp_poll_option_unique_hash UNIQUE (bridge_id, msg_mxid, opt_hash),
    CONSTRAINT message_mxid_fkey FOREIGN KEY (bridge_id, msg_mxid)
        REFERENCES message(bridge_id, mxid) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE whatsapp_history_sync_conversation (
    bridge_id     TEXT,
    user_login_id TEXT,
    chat_jid      TEXT,

    last_message_timestamp       BIGINT,
    archived                     BOOLEAN,
    pinned                       BOOLEAN,
    mute_end_time                BIGINT,
    end_of_history_transfer_type INTEGER,
    ephemeral_expiration         INTEGER,
    ephemeral_setting_timestamp  BIGINT,
    marked_as_unread             BOOLEAN,
    unread_count                 INTEGER,

    PRIMARY KEY (bridge_id, user_login_id, chat_jid),
    CONSTRAINT whatsapp_history_sync_conversation_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login(bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE whatsapp_history_sync_message (
    bridge_id     TEXT,
    user_login_id TEXT,
    chat_jid      TEXT,
    message_id    TEXT,
    timestamp     BIGINT,
    data          bytea,
    inserted_time BIGINT,

    PRIMARY KEY (bridge_id, user_login_id, chat_jid, message_id),
    CONSTRAINT whatsapp_history_sync_message_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login(bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT whatsapp_history_sync_message_conversation_fkey FOREIGN KEY (bridge_id, user_login_id, chat_jid)
        REFERENCES whatsapp_history_sync_conversation(bridge_id, user_login_id, chat_jid) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE whatsapp_media_backfill_request (
    bridge_id       TEXT,
    user_login_id   TEXT,
    portal_id       TEXT,
    portal_receiver TEXT,

    event_id        TEXT,
    media_key       bytea,
    status          INTEGER,
    error           TEXT,

    PRIMARY KEY (bridge_id, user_login_id, portal_id, portal_receiver, event_id),
    CONSTRAINT whatsapp_media_backfill_request_user_login_fkey FOREIGN KEY (bridge_id, user_login_id)
        REFERENCES user_login(bridge_id, id) ON UPDATE CASCADE ON DELETE CASCADE,
    CONSTRAINT whatsapp_media_backfill_request_portal_fkey FOREIGN KEY (bridge_id, portal_id, portal_receiver)
        REFERENCES portal(bridge_id, id, receiver) ON UPDATE CASCADE ON DELETE CASCADE
);
