-- v46: Add inserted time to history sync message

ALTER TABLE history_sync_message ADD COLUMN inserted_time TIMESTAMP;
