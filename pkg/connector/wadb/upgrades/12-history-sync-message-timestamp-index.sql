-- v12 (compatible with v3+): Add index for history sync message timestamp lookups
CREATE INDEX whatsapp_history_sync_message_chat_ts_idx ON whatsapp_history_sync_message (bridge_id, user_login_id, chat_jid, timestamp);
