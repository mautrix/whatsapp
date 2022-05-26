-- v43: Add table for tracking which media needs to be requested from the user's phone

CREATE TABLE media_backfill_requests (
    user_mxid       TEXT,
    portal_jid      TEXT,
    portal_receiver TEXT,
    event_id        TEXT,
    media_key       BYTEA,
    status          INTEGER,
    error           TEXT,
    PRIMARY KEY (user_mxid, portal_jid, portal_receiver, event_id),
    FOREIGN KEY (user_mxid) REFERENCES "user" (mxid) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal (jid, receiver) ON DELETE CASCADE
);
