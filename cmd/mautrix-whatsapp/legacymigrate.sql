INSERT INTO "user" (bridge_id, mxid, management_room, access_token)
SELECT '', mxid, management_room, ''
FROM user_old;

UPDATE "user" SET access_token=COALESCE((SELECT access_token FROM puppet_old WHERE custom_mxid="user".mxid AND access_token<>'' LIMIT 1), '');

INSERT INTO user_login (bridge_id, user_mxid, id, remote_name, space_room, metadata, remote_profile)
SELECT
    '', -- bridge_id
    mxid, -- user_mxid
    username, -- id
    '+' || username, -- remote_name
    space_room,
    -- only: postgres
    jsonb_build_object
-- only: sqlite (line commented)
--  json_object
    (
        'wa_device_id', device,
        'phone_last_seen', phone_last_seen,
        'phone_last_pinged', phone_last_pinged,
        'timezone', timezone
    ), -- metadata
    '{}' -- remote_profile
FROM user_old
WHERE username<>'' AND device<>0;

INSERT INTO ghost (
    bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc,
    name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata
)
SELECT
    '', -- bridge_id
    username, -- id
    COALESCE(displayname, ''), -- name
    COALESCE(avatar, ''), -- avatar_id
    '', -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    contact_info_set,
    false, -- is_bot
    '[]', -- identifiers
    -- only: postgres
    jsonb_build_object
    -- only: sqlite (line commented)
--  json_object
    (
        'last_sync', last_sync
        -- TODO name quality
    ) -- metadata
FROM puppet_old;

INSERT INTO ghost (bridge_id, id, name, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, contact_info_set, is_bot, identifiers, metadata)
VALUES ('', '', '', '', '', '', false, false, false, false, '[]', '{}');

INSERT INTO portal (
    bridge_id, id, receiver, mxid, parent_id, parent_receiver, relay_bridge_id, relay_login_id, other_user_id,
    name, topic, avatar_id, avatar_hash, avatar_mxc, name_set, avatar_set, topic_set,
    name_is_custom, in_space, room_type, disappear_type, disappear_timer, metadata
)
SELECT
    '', -- bridge_id
    jid, -- id
    CASE WHEN receiver LIKE '%@s.whatsapp.net' THEN replace(receiver, '@s.whatsapp.net', '') ELSE '' END, -- receiver
    mxid,
    CASE WHEN EXISTS(SELECT 1 FROM portal_old WHERE jid=parent_group) THEN parent_group ELSE NULL END, -- parent_id
    '', -- parent_receiver
    CASE WHEN relay_user_id<>'' THEN '' END, -- relay_bridge_id
    (SELECT id FROM user_login WHERE user_mxid=relay_user_id), -- relay_login_id
    CASE WHEN jid LIKE '%@s.whatsapp.net' THEN replace(jid, '@s.whatsapp.net', '') ELSE '' END, -- other_user_id
    name,
    topic,
    avatar, -- avatar_id
    '', -- avatar_hash
    COALESCE(avatar_url, ''), -- avatar_mxc
    name_set,
    avatar_set,
    topic_set,
    jid NOT LIKE '%@s.whatsapp.net', -- name_is_custom
    in_space,
    CASE
        WHEN is_parent THEN 'space'
        WHEN jid LIKE '%@s.whatsapp.net' THEN 'dm'
        ELSE ''
    END, -- room_type
    CASE WHEN expiration_time>0 THEN 'after_read' END, -- disappear_type
    CASE WHEN expiration_time > 0 THEN expiration_time * 1000000000 END, -- disappear_timer
    -- only: postgres
    jsonb_build_object
    -- only: sqlite (line commented)
--  json_object
    (
        'last_sync', last_sync
    ) -- metadata
FROM portal_old;

INSERT INTO user_portal (bridge_id, user_mxid, login_id, portal_id, portal_receiver, in_space, preferred, last_read)
SELECT
    '', -- bridge_id
    user_mxid,
    (SELECT id FROM user_login WHERE user_login.user_mxid=user_portal_old.user_mxid), -- login_id
    portal_jid, -- portal_id
    CASE WHEN portal_receiver LIKE '%@s.whatsapp.net' THEN replace(portal_receiver, '@s.whatsapp.net', '') ELSE '' END, -- portal_receiver
    in_space,
    false, -- preferred
    last_read_ts * 1000000000 -- last_read
FROM user_portal_old WHERE EXISTS(SELECT 1 FROM user_login WHERE user_login.user_mxid=user_portal_old.user_mxid);

ALTER TABLE message_old ADD COLUMN combined_id TEXT;
DELETE FROM message_old WHERE sender IS NULL;
UPDATE message_old SET combined_id = chat_jid || ':' || (
    CASE WHEN sender LIKE '%:%@s.whatsapp.net'
        THEN (split_part(replace(sender, '@s.whatsapp.net', ''), ':', 1) || '@s.whatsapp.net')
        ELSE sender
    END
) || ':' || jid;
DELETE FROM message_old WHERE timestamp<0;
-- only: sqlite for next 2 lines
DELETE FROM message_old WHERE rowid IN (SELECT rowid FROM pragma_foreign_key_check('message_old'));
DELETE FROM reaction_old WHERE rowid IN (SELECT rowid FROM pragma_foreign_key_check('reaction_old'));

INSERT INTO message (
    bridge_id, id, part_id, mxid, room_id, room_receiver, sender_id, sender_mxid, timestamp, edit_count, metadata
)
SELECT
    '', -- bridge_id
    combined_id, -- id
    '', -- part_id
    mxid,
    chat_jid, -- room_id
    CASE WHEN chat_receiver LIKE '%@s.whatsapp.net' THEN replace(chat_receiver, '@s.whatsapp.net', '') ELSE '' END, -- room_receiver
    CASE WHEN sender=chat_jid AND sender NOT LIKE '%@s.whatsapp.net'
        THEN ''
        ELSE split_part(split_part(replace(sender, '@s.whatsapp.net', ''), ':', 1), '.', 1)
    END, -- sender_id
    sender_mxid, -- sender_mxid
    timestamp * 1000000000, -- timestamp
    0, -- edit_count
    -- only: postgres
    jsonb_build_object
    -- only: sqlite (line commented)
--  json_object
    (
        'sender_device_id', CAST(nullif(split_part(replace(sender, '@s.whatsapp.net', ''), ':', 2), '') AS INTEGER),
        'broadcast_list_jid', broadcast_list_jid,
        'error', CAST(error AS TEXT)
    ) -- metadata
FROM message_old;

INSERT INTO reaction (
    bridge_id, message_id, message_part_id, sender_id, emoji_id, room_id, room_receiver, mxid, timestamp, emoji, metadata
)
SELECT
    '', -- bridge_id
    message_old.combined_id, -- message_id
    '', -- message_part_id
    replace(reaction_old.sender, '@s.whatsapp.net', ''), -- sender_id
    '', -- emoji_id
    reaction_old.chat_jid, -- room_id
    CASE WHEN reaction_old.chat_receiver LIKE '%@s.whatsapp.net' THEN replace(reaction_old.chat_receiver, '@s.whatsapp.net', '') ELSE '' END, -- room_receiver
    reaction_old.mxid,
    0, -- timestamp
    '', -- emoji
    -- only: postgres
    jsonb_build_object
    -- only: sqlite (line commented)
--  json_object
    (
        'sender_device_id', CAST(nullif(split_part(replace(reaction_old.sender, '@s.whatsapp.net', ''), ':', 2), '') AS INTEGER)
    ) -- metadata
FROM reaction_old
LEFT JOIN message_old
    ON reaction_old.chat_jid = message_old.chat_jid
    AND reaction_old.chat_receiver = message_old.chat_receiver
    AND reaction_old.target_jid = message_old.jid;

INSERT INTO disappearing_message (bridge_id, mx_room, mxid, type, timer, disappear_at)
SELECT
    '', -- bridge_id
    room_id,
    event_id,
    'after_read',
    expire_in * 1000000, -- timer
    expire_at * 1000000 -- disappear_at
FROM disappearing_message_old;

INSERT INTO backfill_task (
    bridge_id, portal_id, portal_receiver, user_login_id, batch_count, is_done,
    cursor, oldest_message_id, dispatched_at, completed_at, next_dispatch_min_ts
)
SELECT
    '', -- bridge_id
    portal_jid, -- portal_id
    CASE WHEN portal_receiver LIKE '%@s.whatsapp.net' THEN replace(portal_receiver, '@s.whatsapp.net', '') ELSE '' END, -- portal_receiver
    (SELECT id FROM user_login WHERE user_login.user_mxid=backfill_queue_old.user_mxid), -- user_login_id
    COUNT(*), -- batch_count
    COUNT(*) = COUNT(completed_at), -- is_done
    '', -- cursor
    '', -- oldest_message_id
    -- only: postgres
    EXTRACT(EPOCH FROM MAX(dispatch_time)) * 1000000000, -- dispatched_at
    -- only: sqlite (line commented)
--  unixepoch(MAX(dispatch_time)) * 1000000000,
    NULL, -- completed_at
    1 -- next_dispatch_min_ts
FROM backfill_queue_old
WHERE type IN (0, 200) AND EXISTS(SELECT 1 FROM user_login WHERE user_login.user_mxid=backfill_queue_old.user_mxid)
GROUP BY user_mxid, portal_jid, portal_receiver;

INSERT INTO whatsapp_poll_option_id (bridge_id, msg_mxid, opt_id, opt_hash)
SELECT '', msg_mxid, opt_id, opt_hash
FROM poll_option_id_old;

INSERT INTO whatsapp_history_sync_conversation (
    bridge_id, user_login_id, chat_jid, last_message_timestamp, archived, pinned, mute_end_time,
    end_of_history_transfer_type, ephemeral_expiration, ephemeral_setting_timestamp, marked_as_unread, unread_count
)
SELECT
    '',
    user_login.id,
    portal_jid,
    -- only: postgres
    CAST(EXTRACT(EPOCH FROM last_message_timestamp) AS BIGINT),
    -- only: sqlite (line commented)
--  unixepoch(last_message_timestamp),
    archived,
    CASE WHEN pinned > 0 THEN true ELSE false END,
    -- only: postgres
    CAST(EXTRACT(EPOCH FROM mute_end_time) AS BIGINT),
    -- only: sqlite (line commented)
--  unixepoch(mute_end_time),
    end_of_history_transfer_type,
    ephemeral_expiration,
    0,
    marked_as_unread,
    unread_count
FROM history_sync_conversation_old
LEFT JOIN user_login ON user_login.user_mxid = history_sync_conversation_old.user_mxid
WHERE user_login.id IS NOT NULL;

INSERT INTO whatsapp_history_sync_message (
    bridge_id, user_login_id, chat_jid, sender_jid, message_id, timestamp, data, inserted_time
)
SELECT
    '',
    user_login.id,
    conversation_id,
    message_id,
    '',
    -- only: postgres
    CAST(EXTRACT(EPOCH FROM timestamp) AS BIGINT),
    -- only: sqlite (line commented)
--  unixepoch(timestamp),
    data,
    -- only: postgres
    CAST(EXTRACT(EPOCH FROM inserted_time) AS BIGINT)
    -- only: sqlite (line commented)
--  unixepoch(inserted_time)
FROM history_sync_message_old
LEFT JOIN user_login ON user_login.user_mxid = history_sync_message_old.user_mxid
WHERE user_login.id IS NOT NULL;

INSERT INTO whatsapp_media_backfill_request (
    bridge_id, user_login_id, message_id, portal_id, portal_receiver, media_key, status, error
)
SELECT
    '',
    user_login.id,
    (SELECT id FROM message WHERE mxid=event_id),
    portal_jid,
    CASE WHEN portal_receiver LIKE '%@s.whatsapp.net' THEN replace(portal_receiver, '@s.whatsapp.net', '') ELSE '' END,
    media_key,
    status,
    COALESCE(error, '')
FROM media_backfill_requests_old
LEFT JOIN user_login ON user_login.user_mxid = media_backfill_requests_old.user_mxid
WHERE user_login.id IS NOT NULL AND status IS NOT NULL AND EXISTS (SELECT 1 FROM message WHERE mxid=event_id);

DROP TABLE backfill_queue_old;
DROP TABLE backfill_state_old;
DROP TABLE disappearing_message_old;
DROP TABLE history_sync_message_old;
DROP TABLE history_sync_conversation_old;
DROP TABLE media_backfill_requests_old;
DROP TABLE poll_option_id_old;
DROP TABLE user_portal_old;
DROP TABLE reaction_old;
DROP TABLE message_old;
DROP TABLE puppet_old;
DROP TABLE portal_old;
DROP TABLE user_old;
