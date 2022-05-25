-- v47: Add table for keeping track of backfill state

CREATE TABLE backfill_state (
    user_mxid         TEXT,
    portal_jid        TEXT,
    portal_receiver   TEXT,
    processing_batch  BOOLEAN,
    backfill_complete BOOLEAN,
    first_expected_ts INTEGER,
    PRIMARY KEY (user_mxid, portal_jid, portal_receiver),
    FOREIGN KEY (user_mxid) REFERENCES "user" (mxid) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal (jid, receiver) ON DELETE CASCADE
);
