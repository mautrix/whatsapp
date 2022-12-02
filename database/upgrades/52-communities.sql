-- v52: Store portal metadata for communities

ALTER TABLE portal ADD COLUMN is_parent BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE portal ADD COLUMN parent_group TEXT;
ALTER TABLE portal ADD COLUMN in_space BOOLEAN NOT NULL DEFAULT false;
