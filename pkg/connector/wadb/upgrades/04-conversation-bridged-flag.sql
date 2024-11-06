-- v4 (compatible with v3+): Add bridged flag for history sync conversations
ALTER TABLE whatsapp_history_sync_conversation ADD COLUMN bridged BOOLEAN NOT NULL DEFAULT false;
