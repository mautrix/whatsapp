-- v7 (compatible with v3+): Add cache for avatar URLs when using direct media
CREATE TABLE whatsapp_avatar_cache (
    entity_jid  TEXT    NOT NULL,
    avatar_id   TEXT    NOT NULL,
    direct_path TEXT    NOT NULL,
    expiry      BIGINT  NOT NULL,
    gone        BOOLEAN NOT NULL DEFAULT false,

    PRIMARY KEY (entity_jid, avatar_id)
);
