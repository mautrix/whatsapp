-- v0 -> v57 (compatible with v45+): Latest revision

CREATE TABLE "user" (
    mxid     TEXT PRIMARY KEY,
    username TEXT UNIQUE,
    agent    SMALLINT,
    device   SMALLINT,

    management_room TEXT,
    space_room      TEXT,

    phone_last_seen   BIGINT,
    phone_last_pinged BIGINT,

    timezone TEXT
);

CREATE TABLE portal (
    jid        TEXT,
    receiver   TEXT,
    mxid       TEXT UNIQUE,
    name       TEXT    NOT NULL,
    name_set   BOOLEAN NOT NULL DEFAULT false,
    topic      TEXT    NOT NULL,
    topic_set  BOOLEAN NOT NULL DEFAULT false,
    avatar     TEXT    NOT NULL,
    avatar_url TEXT,
    avatar_set BOOLEAN NOT NULL DEFAULT false,
    encrypted  BOOLEAN NOT NULL DEFAULT false,
    last_sync  BIGINT NOT NULL DEFAULT 0,

    is_parent    BOOLEAN NOT NULL DEFAULT false,
    parent_group TEXT,
    in_space     BOOLEAN NOT NULL DEFAULT false,

    first_event_id  TEXT,
    next_batch_id   TEXT,
    relay_user_id   TEXT,
    expiration_time BIGINT NOT NULL DEFAULT 0 CHECK (expiration_time >= 0 AND expiration_time < 4294967296),

    PRIMARY KEY (jid, receiver)
);
CREATE INDEX portal_parent_group_idx ON portal(parent_group);

CREATE TABLE puppet (
    username         TEXT PRIMARY KEY,
    displayname      TEXT,
    name_quality     SMALLINT,
    avatar           TEXT,
    avatar_url       TEXT,
    name_set         BOOLEAN NOT NULL DEFAULT false,
    avatar_set       BOOLEAN NOT NULL DEFAULT false,
    contact_info_set BOOLEAN NOT NULL DEFAULT false,
    last_sync        BIGINT NOT NULL DEFAULT 0,

    custom_mxid  TEXT,
    access_token TEXT,
    next_batch   TEXT,

    enable_presence BOOLEAN NOT NULL DEFAULT true,
    enable_receipts BOOLEAN NOT NULL DEFAULT true
);

-- only: postgres
CREATE TYPE error_type AS ENUM ('', 'decryption_failed', 'media_not_found');

CREATE TABLE message (
    chat_jid      TEXT,
    chat_receiver TEXT,
    jid           TEXT,
    mxid          TEXT UNIQUE,
    sender        TEXT,
    sender_mxid   TEXT NOT NULL DEFAULT '',
    timestamp     BIGINT,
    sent          BOOLEAN,
    error         error_type,
    type          TEXT,

    broadcast_list_jid TEXT,

    PRIMARY KEY (chat_jid, chat_receiver, jid),
    FOREIGN KEY (chat_jid, chat_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
);

CREATE INDEX message_timestamp_idx ON message (chat_jid, chat_receiver, timestamp);

CREATE TABLE poll_option_id (
    msg_mxid TEXT,
    opt_id   TEXT,
    opt_hash bytea CHECK ( length(opt_hash) = 32 ),

    PRIMARY KEY (msg_mxid, opt_id),
    CONSTRAINT poll_option_unique_hash UNIQUE (msg_mxid, opt_hash),
    CONSTRAINT message_mxid_fkey FOREIGN KEY (msg_mxid) REFERENCES message(mxid) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE reaction (
    chat_jid      TEXT,
    chat_receiver TEXT,
    target_jid    TEXT,
    sender        TEXT,

    mxid TEXT NOT NULL,
    jid  TEXT NOT NULL,

    PRIMARY KEY (chat_jid, chat_receiver, target_jid, sender),
    FOREIGN KEY (chat_jid, chat_receiver, target_jid) REFERENCES message(chat_jid, chat_receiver, jid)
        ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE disappearing_message (
    room_id   TEXT,
    event_id  TEXT,
    expire_in BIGINT NOT NULL,
    expire_at BIGINT,
    PRIMARY KEY (room_id, event_id)
);

CREATE TABLE user_portal (
    user_mxid       TEXT,
    portal_jid      TEXT,
    portal_receiver TEXT,
    last_read_ts    BIGINT  NOT NULL DEFAULT 0,
    in_space        BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (user_mxid, portal_jid, portal_receiver),
    FOREIGN KEY (user_mxid)                   REFERENCES "user"(mxid)          ON UPDATE CASCADE ON DELETE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE backfill_queue (
    queue_id INTEGER PRIMARY KEY
        -- only: postgres
        GENERATED ALWAYS AS IDENTITY
        ,
    user_mxid        TEXT,
    type             INTEGER NOT NULL,
    priority         INTEGER NOT NULL,
    portal_jid       TEXT,
    portal_receiver  TEXT,
    time_start       TIMESTAMP,
    dispatch_time    TIMESTAMP,
    completed_at     TIMESTAMP,
    batch_delay      INTEGER,
    max_batch_events INTEGER NOT NULL,
    max_total_events INTEGER,

    FOREIGN KEY (user_mxid) REFERENCES "user" (mxid) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
);

CREATE TABLE backfill_state (
    user_mxid         TEXT,
    portal_jid        TEXT,
    portal_receiver   TEXT,
    processing_batch  BOOLEAN,
    backfill_complete BOOLEAN,
    first_expected_ts BIGINT,
    PRIMARY KEY (user_mxid, portal_jid, portal_receiver),
    FOREIGN KEY (user_mxid) REFERENCES "user" (mxid) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal (jid, receiver) ON DELETE CASCADE
);

CREATE TABLE media_backfill_requests (
    user_mxid       TEXT,
    portal_jid      TEXT,
    portal_receiver TEXT,
    event_id        TEXT,
    media_key       bytea,
    status          INTEGER,
    error           TEXT,
    PRIMARY KEY (user_mxid, portal_jid, portal_receiver, event_id),
    FOREIGN KEY (user_mxid)                   REFERENCES "user"(mxid)          ON UPDATE CASCADE ON DELETE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE history_sync_conversation (
    user_mxid       TEXT,
    conversation_id TEXT,
    portal_jid      TEXT,
    portal_receiver TEXT,

    last_message_timestamp       TIMESTAMP,
    archived                     BOOLEAN,
    pinned                       INTEGER,
    mute_end_time                TIMESTAMP,
    disappearing_mode            INTEGER,
    end_of_history_transfer_type INTEGER,
    ephemeral_Expiration         INTEGER,
    marked_as_unread             BOOLEAN,
    unread_count                 INTEGER,

    PRIMARY KEY (user_mxid, conversation_id),
    FOREIGN KEY (user_mxid)                   REFERENCES "user"(mxid)          ON UPDATE CASCADE ON DELETE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON UPDATE CASCADE ON DELETE CASCADE
);

CREATE TABLE history_sync_message (
    user_mxid       TEXT,
    conversation_id TEXT,
    message_id      TEXT,
    timestamp       TIMESTAMP,
    data            bytea,
    inserted_time   TIMESTAMP,

    PRIMARY KEY (user_mxid, conversation_id, message_id),
    FOREIGN KEY (user_mxid)                  REFERENCES "user"(mxid) ON UPDATE CASCADE ON DELETE CASCADE,
    FOREIGN KEY (user_mxid, conversation_id) REFERENCES history_sync_conversation(user_mxid, conversation_id) ON DELETE CASCADE
);
