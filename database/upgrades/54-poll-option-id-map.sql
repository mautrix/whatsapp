-- v54: Store mapping for poll option IDs from Matrix

CREATE TABLE poll_option_id (
    msg_mxid TEXT,
    opt_id   TEXT,
    opt_hash bytea CHECK ( length(opt_hash) = 32 ),

    PRIMARY KEY (msg_mxid, opt_id),
    CONSTRAINT poll_option_unique_hash UNIQUE (msg_mxid, opt_hash),
    CONSTRAINT message_mxid_fkey FOREIGN KEY (msg_mxid) REFERENCES message(mxid) ON DELETE CASCADE ON UPDATE CASCADE
);
