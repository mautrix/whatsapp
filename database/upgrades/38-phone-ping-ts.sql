-- v38: Store timestamp for previous phone ping

ALTER TABLE "user" ADD COLUMN phone_last_pinged BIGINT;
