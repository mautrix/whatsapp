-- v11 (compatible with v3+): Persist MatrixRTC reaction relation targets
ALTER TABLE whatsapp_matrixrtc_call ADD COLUMN bridge_membership_event_id TEXT;
ALTER TABLE whatsapp_matrixrtc_call ADD COLUMN selected_membership_event_id TEXT;
ALTER TABLE whatsapp_matrixrtc_call ADD COLUMN bridge_hand_raise_event_id TEXT;
ALTER TABLE whatsapp_matrixrtc_call ADD COLUMN selected_hand_raise_event_id TEXT;
