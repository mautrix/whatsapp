-- v55: Store whether custom contact info has been set for a puppet

ALTER TABLE puppet ADD COLUMN contact_info_set BOOLEAN NOT NULL DEFAULT false;
