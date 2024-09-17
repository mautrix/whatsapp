-- v2: Add sender JID to history sync messages
-- transaction: sqlite-fkey-off
ALTER TABLE whatsapp_history_sync_message ADD COLUMN sender_jid TEXT NOT NULL DEFAULT '';
ALTER TABLE whatsapp_history_sync_message ALTER COLUMN sender_jid DROP DEFAULT;
ALTER TABLE whatsapp_history_sync_message DROP CONSTRAINT whatsapp_history_sync_message_pkey;
ALTER TABLE whatsapp_history_sync_message ADD PRIMARY KEY (bridge_id, user_login_id, chat_jid, sender_jid, message_id);
