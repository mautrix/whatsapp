-- v37: Store message error type as string

-- only: postgres
CREATE TYPE error_type AS ENUM ('', 'decryption_failed', 'media_not_found');

ALTER TABLE message ADD COLUMN error error_type NOT NULL DEFAULT '';
UPDATE message SET error='decryption_failed' WHERE decryption_error=true;

-- TODO do this on sqlite at some point
-- only: postgres
ALTER TABLE message DROP COLUMN decryption_error;
