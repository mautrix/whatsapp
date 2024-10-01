-- v3: Change primary key of media backfill request table

-- only: postgres for next 2 lines
ALTER TABLE whatsapp_media_backfill_request DROP CONSTRAINT whatsapp_media_backfill_request_user_login_fkey;
ALTER TABLE whatsapp_media_backfill_request DROP CONSTRAINT whatsapp_media_backfill_request_portal_fkey;

CREATE TABLE whatsapp_media_backfill_request_new (
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

INSERT INTO whatsapp_media_backfill_request_new (bridge_id, user_login_id, message_id, portal_id, portal_receiver, media_key, status, error)
SELECT bridge_id, user_login_id, (SELECT id FROM message WHERE mxid=event_id), portal_id, portal_receiver,  media_key, status, error
FROM whatsapp_media_backfill_request WHERE EXISTS(SELECT 1 FROM message WHERE mxid=event_id);

DROP TABLE whatsapp_media_backfill_request;
ALTER TABLE whatsapp_media_backfill_request_new RENAME TO whatsapp_media_backfill_request;

CREATE INDEX whatsapp_media_backfill_request_portal_idx ON whatsapp_media_backfill_request (bridge_id, portal_id, portal_receiver);
CREATE INDEX whatsapp_media_backfill_request_message_idx ON whatsapp_media_backfill_request (bridge_id, portal_receiver, message_id, _part_id);
-- only: postgres
ALTER TABLE whatsapp_media_backfill_request RENAME CONSTRAINT whatsapp_media_backfill_request_new_pkey TO whatsapp_media_backfill_request_pkey;
