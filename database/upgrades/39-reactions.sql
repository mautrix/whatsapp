-- v39: Add support for reactions

ALTER TABLE message ADD COLUMN type TEXT NOT NULL DEFAULT 'message';
-- only: postgres
ALTER TABLE message ALTER COLUMN type DROP DEFAULT;

UPDATE message SET type='' WHERE error='decryption_failed';
UPDATE message SET type='fake' WHERE jid LIKE 'FAKE::%' OR mxid LIKE 'net.maunium.whatsapp.fake::%' OR jid=mxid;

CREATE TABLE reaction (
    chat_jid      TEXT,
    chat_receiver TEXT,
    target_jid    TEXT,
    sender        TEXT,
    mxid          TEXT NOT NULL,
    jid           TEXT NOT NULL,
    PRIMARY KEY (chat_jid, chat_receiver, target_jid, sender),
    CONSTRAINT target_message_fkey FOREIGN KEY (chat_jid, chat_receiver, target_jid)
        REFERENCES message (chat_jid, chat_receiver, jid)
        ON DELETE CASCADE ON UPDATE CASCADE
);
