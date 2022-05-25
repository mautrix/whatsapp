-- v49: Convert first_expected_ts to BIGINT so that we can use zero-values.

-- only: sqlite
UPDATE backfill_state SET first_expected_ts=unixepoch(first_expected_ts);

-- only: postgres
ALTER TABLE backfill_state ALTER COLUMN first_expected_ts TYPE BIGINT USING extract(epoch from first_expected_ts);
