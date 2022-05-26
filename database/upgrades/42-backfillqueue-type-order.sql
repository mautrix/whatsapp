-- v42: Update backfill queue tables to be sortable by priority

UPDATE backfill_queue
SET type=CASE
    WHEN type = 1 THEN 200
    WHEN type = 2 THEN 300
    ELSE type
END
WHERE type = 1 OR type = 2;
