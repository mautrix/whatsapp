-- v11 (compatible with v3+): Re-arm backfill for chats with leftover history sync messages
UPDATE backfill_task
SET is_done=false, queue_done=false, dispatched_at=NULL, completed_at=NULL
WHERE (is_done OR queue_done) AND EXISTS (
    SELECT 1
    FROM whatsapp_history_sync_message msg
    WHERE msg.bridge_id=backfill_task.bridge_id
      AND msg.user_login_id=backfill_task.user_login_id
      AND msg.chat_jid=backfill_task.portal_id
);
