-- v51: Add last sync timestamp for portals too

ALTER TABLE portal ADD COLUMN last_sync BIGINT NOT NULL DEFAULT 0;
