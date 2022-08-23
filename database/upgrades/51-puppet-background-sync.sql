-- v50: Add last sync timestamp for puppets

ALTER TABLE puppet ADD COLUMN last_sync BIGINT NOT NULL DEFAULT 0;
ALTER TABLE puppet ADD COLUMN name_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE puppet ADD COLUMN avatar_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE portal ADD COLUMN name_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE portal ADD COLUMN avatar_set BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE portal ADD COLUMN topic_set BOOLEAN NOT NULL DEFAULT false;
UPDATE puppet SET name_set=true WHERE displayname<>'';
UPDATE puppet SET avatar_set=true WHERE avatar<>'';
UPDATE portal SET name_set=true WHERE name<>'';
UPDATE portal SET avatar_set=true WHERE avatar<>'';
UPDATE portal SET topic_set=true WHERE topic<>'';
