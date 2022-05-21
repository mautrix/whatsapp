-- v45: Add dispatch time to backfill queue

ALTER TABLE backfill_queue ADD COLUMN dispatch_time TIMESTAMP;
UPDATE backfill_queue SET dispatch_time=completed_at;
ALTER TABLE backfill_queue DROP COLUMN time_end;
