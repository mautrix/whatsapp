-- v6 (compatible with v3+): Store timestamp for which login a conversation was synced with
ALTER TABLE whatsapp_history_sync_conversation ADD COLUMN synced_login_ts BIGINT;
UPDATE whatsapp_history_sync_conversation SET synced_login_ts = 0 WHERE bridged = true;
ALTER TABLE whatsapp_history_sync_conversation DROP COLUMN bridged;
