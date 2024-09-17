-- v2: Add sender JID to history sync messages
-- transaction: sqlite-fkey-off

CREATE TABLE whatsapp_history_sync_message_new (
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

INSERT INTO whatsapp_history_sync_message_new (
    bridge_id, user_login_id, chat_jid, sender_jid, message_id, timestamp, data, inserted_time
)
SELECT
    bridge_id,
    user_login_id,
    chat_jid,
    message_id,
    '',
    timestamp,
    data,
    inserted_time
FROM whatsapp_history_sync_message;

DROP TABLE whatsapp_history_sync_message;
ALTER TABLE whatsapp_history_sync_message_new RENAME TO whatsapp_history_sync_message;
