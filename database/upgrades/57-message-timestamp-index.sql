-- v57 (compatible with v45+): Add index for message timestamp to make read receipt handling faster
CREATE INDEX message_timestamp_idx ON message (chat_jid, chat_receiver, timestamp);
