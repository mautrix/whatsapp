-- v58 (compatible with v46+): Add index for message timestamp to make read receipt handling faster
CREATE INDEX message_timestamp_idx ON message (chat_jid, chat_receiver, timestamp);
