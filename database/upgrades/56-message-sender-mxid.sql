-- v56 (compatible with v45+): Store whether custom contact info has been set for a puppet
ALTER TABLE message ADD COLUMN sender_mxid TEXT NOT NULL DEFAULT '';
