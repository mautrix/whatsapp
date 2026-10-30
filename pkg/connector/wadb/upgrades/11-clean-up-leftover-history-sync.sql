-- v11 (compatible with v3+): Clean up history sync messages left behind by old backfill bugs

-- Backfill only extends rooms outwards, so buffered messages inside the bridged range will never be used
WITH bridged_range AS (
    SELECT bridge_id, room_id, room_receiver, MIN(timestamp) AS oldest, MAX(timestamp) AS newest
    FROM message
    GROUP BY bridge_id, room_id, room_receiver
)
DELETE FROM whatsapp_history_sync_message
WHERE EXISTS (
    SELECT 1
    FROM bridged_range
    WHERE bridged_range.bridge_id=whatsapp_history_sync_message.bridge_id
      AND bridged_range.room_id=whatsapp_history_sync_message.chat_jid
      AND bridged_range.room_receiver IN (whatsapp_history_sync_message.user_login_id, '')
      AND whatsapp_history_sync_message.timestamp * 1000000000 BETWEEN bridged_range.oldest AND bridged_range.newest
);

-- History sync can carry an older timestamp than the bridged copy, so also drop anything already bridged by ID
DELETE FROM whatsapp_history_sync_message
WHERE EXISTS (
    SELECT 1
    FROM message
    WHERE message.bridge_id=whatsapp_history_sync_message.bridge_id
      AND message.room_id=whatsapp_history_sync_message.chat_jid
      AND message.room_receiver IN (whatsapp_history_sync_message.user_login_id, '')
      AND message.id LIKE '%:' || whatsapp_history_sync_message.message_id
);

-- Re-arm backfill for chats that still have older messages buffered
UPDATE backfill_task
SET is_done=false, queue_done=false, dispatched_at=NULL, completed_at=NULL
WHERE (is_done OR queue_done) AND EXISTS (
    SELECT 1
    FROM whatsapp_history_sync_message msg
    WHERE msg.bridge_id=backfill_task.bridge_id
      AND msg.user_login_id=backfill_task.user_login_id
      AND msg.chat_jid=backfill_task.portal_id
);
