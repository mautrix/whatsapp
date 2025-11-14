-- v8 (compatible with v3+): Mark LID DMs for deletion
INSERT INTO kv_store (bridge_id, key, value) VALUES ('', 'whatsapp_lid_dms_deleted', 'false');
