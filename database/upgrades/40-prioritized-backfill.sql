-- v40: Add backfill queue

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
    time_end         TIMESTAMP,
    completed_at     TIMESTAMP,
    batch_delay      INTEGER,
    max_batch_events INTEGER NOT NULL,
    max_total_events INTEGER,

    FOREIGN KEY (user_mxid) REFERENCES "user" (mxid) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (portal_jid, portal_receiver) REFERENCES portal(jid, receiver) ON DELETE CASCADE
);
