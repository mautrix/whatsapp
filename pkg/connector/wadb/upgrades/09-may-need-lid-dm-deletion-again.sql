-- v9 (compatible with v3+): Mark LID DMs for deletion (again)
DELETE FROM kv_store WHERE bridge_id='' AND key='whatsapp_lid_dms_deleted';
INSERT INTO kv_store (bridge_id, key, value) VALUES ('', 'whatsapp_lid_dms_deleted', 'false');
