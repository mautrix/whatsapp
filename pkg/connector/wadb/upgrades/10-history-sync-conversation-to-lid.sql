-- v10 (compatible with v3+): Move history sync conversations to LIDs
UPDATE whatsapp_history_sync_conversation
SET chat_jid=COALESCE(
    (SELECT lid || '@lid' FROM whatsmeow_lid_map WHERE pn=replace(chat_jid, '@s.whatsapp.net', '')),
    chat_jid
)
WHERE chat_jid LIKE '%@s.whatsapp.net';
DELETE FROM portal WHERE id LIKE '%@s.whatsapp.net' AND (mxid IS NULL OR mxid='') AND room_type='';
