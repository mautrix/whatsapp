-- v10 (compatible with v3+): Move history sync conversations to LIDs

-- Delete history sync conversations where a @lid conversation already exists
DELETE FROM whatsapp_history_sync_conversation
WHERE chat_jid LIKE '%@lid' AND EXISTS (
    SELECT 1
    FROM whatsapp_history_sync_conversation pnconv
    WHERE pnconv.chat_jid=(
        SELECT pn || '@s.whatsapp.net'
        FROM whatsmeow_lid_map
        WHERE lid=replace(whatsapp_history_sync_conversation.chat_jid, '@lid', '')
    )
);

-- Update all phone number conversations to lids if the lid is known
UPDATE whatsapp_history_sync_conversation
SET chat_jid=(SELECT lid || '@lid' FROM whatsmeow_lid_map WHERE pn=replace(chat_jid, '@s.whatsapp.net', ''))
WHERE chat_jid LIKE '%@s.whatsapp.net'
  AND EXISTS (SELECT 1 FROM whatsmeow_lid_map WHERE pn=replace(chat_jid, '@s.whatsapp.net', ''));

-- Delete blank phone number portals
DELETE FROM portal WHERE id LIKE '%@s.whatsapp.net' AND (mxid IS NULL OR mxid='') AND room_type='';
