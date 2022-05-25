-- v49: Convert first_expected_ts to BIGINT
-- only: postgres

DO
$do$
BEGIN
    IF (SELECT data_type FROM information_schema.columns WHERE table_name='backfill_state' AND column_name='first_expected_ts') = 'integer' THEN
        ALTER TABLE backfill_state ALTER COLUMN first_expected_ts TYPE BIGINT;
    ELSE
        ALTER TABLE backfill_state ALTER COLUMN first_expected_ts TYPE BIGINT USING EXTRACT(EPOCH FROM first_expected_ts);
    END IF;
END
$do$
