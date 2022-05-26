-- v36: Store approximate last seen timestamp of the main device

ALTER TABLE "user" ADD COLUMN phone_last_seen BIGINT;
