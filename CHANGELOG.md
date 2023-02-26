# unreleased

* Bumped minimum Go version to 1.19.
* Switched to zerolog for logging.
  * The basic log config will be migrated automatically, but you may want to
    tweak it as the options are different.

# v0.8.2 (2023-02-16)

* Updated portal room power levels to always allow poll votes.
* Fixed disappearing message timing being implemented incorrectly.
* Fixed server rejecting messages not being handled as an error.
* Fixed sent files not being downloadable on latest WhatsApp beta versions.
* Fixed `sync space` command not syncing DMs into the space properly.
* Added workaround for broken clients like Element iOS that can't render normal
  image messages correctly.

# v0.8.1 (2023-01-16)

* Added support for sending polls from Matrix to WhatsApp.
* Added config option for requesting more history from phone during login.
* Added support for WhatsApp chat with yourself.
* Fixed deleting portals not working correctly in some cases.

# v0.8.0 (2022-12-16)

* Added support for bridging polls from WhatsApp and votes in both directions.
  * Votes are only bridged if MSC3381 polls are enabled
    (`extev_polls` in the config).
* Added support for bridging WhatsApp communities as spaces.
* Updated backfill logic to mark rooms as read if the only message is a notice
  about the disappearing message timer.
* Updated Docker image to Alpine 3.17.
* Fixed backfills starting at the wrong time and sending smaller batches than
  intended in some cases.
* Switched SQLite config from `sqlite3` to `sqlite3-fk-wal` to enforce foreign
  keys and WAL mode. Additionally, adding `_txlock=immediate` to the DB path is
  recommended, but not required.

# v0.7.2 (2022-11-16)

* Added option to handle all transactions asynchronously.
  * This may be useful for large instances, but using it means messages are
    no longer guaranteed to be sent to WhatsApp in the same order as Matrix.
* Fixed database error when backfilling disappearing messages on SQLite.
* Fixed incoming events blocking handling of incoming encryption keys.

# v0.7.1 (2022-10-16)

* Added support for wa.me/qr links in `!wa resolve-link`.
* Added option to sync group members in parallel to speed up syncing large
  groups.
* Added initial support for WhatsApp message editing.
  * Sending edits will be disabled by default until official WhatsApp clients
    start rendering edits.
* Changed `private_chat_portal_meta` config option to be implicitly enabled in
  encrypted rooms, matching the behavior of other mautrix bridges.
* Updated media bridging to check homeserver media size limit before
  downloading media to avoid running out of memory.
  * The bridge may still run out of ram when bridging files if your homeserver
    has a large media size limit and a low bridge memory limit.

# v0.7.0 (2022-09-16)

* Bumped minimum Go version to 1.18.
* Added hidden option to use appservice login for double puppeting.
  * This can be used by adding everyone to a non-exclusive namespace in the
    registration, and setting the login shared secret to the string `appservice`.
* Enabled appservice ephemeral events by default for new installations.
  * Existing bridges can turn it on by enabling `ephemeral_events` and disabling
    `sync_with_custom_puppets` in the config, then regenerating the registration
    file.
* Updated sticker bridging to send actual sticker messages to WhatsApp rather
  than sending as image. This includes converting stickers to webp and adding
  transparent padding to make the aspect ratio 1:1.
* Added automatic webm -> mp4 conversion when sending videos to WhatsApp.
* Started rejecting unsupported mime types when sending media to WhatsApp.
* Added option to use [MSC2409] and [MSC3202] for end-to-bridge encryption.
  However, this may not work with the Synapse implementation as it hasn't
  been tested yet.
* Added error notice if the bridge is started twice.

[MSC3202]: https://github.com/matrix-org/matrix-spec-proposals/pull/3202

# v0.6.1 (2022-08-16)

* Added support for "Delete for me" and deleting private chats from WhatsApp.
* Added support for admin deletions in groups.
* Document with caption messages should work with the bridge as soon as
  WhatsApp enables them in their apps.

# v0.6.0 (2022-07-16)

* Started requiring homeservers to advertise Matrix v1.1 support.
  * This bumps up the minimum homeserver versions to Synapse 1.54 and
    Dendrite 0.8.7. Minimum Conduit version remains at 0.4.0.
  * The bridge will also refuse to start if backfilling is enabled in the
    config, but the homeserver isn't advertising support for MSC2716. Only
    Synapse supports backfilling at the moment.
* Added options to make encryption more secure.
  * The `encryption` -> `verification_levels` config options can be used to
    make the bridge require encrypted messages to come from cross-signed
    devices, with trust-on-first-use validation of the cross-signing master
    key.
  * The `encryption` -> `require` option can be used to make the bridge ignore
    any unencrypted messages.
  * Key rotation settings can be configured with the `encryption` -> `rotation`
    config.
* Added config validation to make the bridge refuse to start if critical fields
  like homeserver or database address haven't been changed from the defaults.
* Added option to include captions in the same message as the media to
  implement [MSC2530]. Sending captions the same way is also supported and
  enabled by default.
* Added basic support for fancy business messages (template and list messages).
* Added periodic background sync of user and group avatars.
* Added maximum message handling duration config options to prevent messages
  getting stuck and blocking everything.
* Changed message send error notices to be replies to the errored message.
* Changed dimensions of stickers bridged from WhatsApp to match WhatsApp web.
* Changed attachment bridging to find the Matrix `msgtype` based on the
  WhatsApp message type instead of the file mimetype.
* Updated Docker image to Alpine 3.16.
* Fixed backfill queue on SQLite.

[MSC2530]: https://github.com/matrix-org/matrix-spec-proposals/pull/2530

# v0.5.0 (2022-06-16)

* Moved a lot of code to mautrix-go.
* Improved handling edge cases in backfill system.
* Improved handling errors in Matrix->WhatsApp message bridging.
* Disallowed sending status broadcast messages by default, as it breaks with
  big contact lists. Sending can be re-enabled in the config.
* Fixed some cases where the first outgoing message was undecryptable for
  WhatsApp users.
* Fixed chats not being marked as read when sending a message from another
  WhatsApp client after receiving a call.
* Fixed other bridge users being added to status broadcasts rooms through
  double puppeting.
* Fixed edge cases in the deferred backfill queue.

# v0.4.0 (2022-05-16)

* Switched from `/r0` to `/v3` paths everywhere.
  * The new `v3` paths are implemented since Synapse 1.48, Dendrite 0.6.5,
    and Conduit 0.4.0. Servers older than these are no longer supported.
* Added new deferred backfill system to allow backfilling historical messages
  later instead of doing everything at login.
* Added option to automatically request old media from phone after backfilling.
* Added experimental provisioning API to check if a phone number is registered
  on WhatsApp.
* Added automatic retrying if the websocket dies while sending a message.
* Added experimental support for sending status broadcast messages.
* Added command to change disappearing message timer in chats.
* Improved error handling if Postgres dies while the bridge is running.
* Fixed bridging stickers sent from WhatsApp web.
* Fixed registration generation not regex-escaping user ID namespaces.

# v0.3.1 (2022-04-16)

* Added emoji normalization for reactions in both directions to add/remove
  variation selector 16 as appropriate.
* Added option to use [MSC2246] async media uploads.
* Fixed custom fields in messages being unencrypted in history syncs.

[MSC2246]: https://github.com/matrix-org/matrix-spec-proposals/pull/2246

# v0.3.0 (2022-03-16)

* Added reaction bridging in both directions.
* Added automatic sending of hidden messages to primary device to prevent
  false-positive disconnection warnings if there have been no messages sent or
  received in >12 days.
* Added proper error message when WhatsApp rejects the connection due to the
  bridge being out of date.
* Added experimental provisioning API to list contacts/groups, start DMs and
  open group portals. Note that these APIs are subject to change at any time.
* Added option to always send "active" delivery receipts (two gray ticks), even
  if presence bridging is disabled. By default, WhatsApp web only sends those
  receipts when it's in the foreground (i.e. showing online status).
* Added option to send online presence on typing notifications (thanks to
  [@abmantis] in [#452]). This can be used to enable incoming typing
  notifications without enabling Matrix presence (WhatsApp only sends typing
  notifications if you're online).
* Added checks to prevent sharing the database with unrelated software.
* Exposed maximum database connection idle time and lifetime options.
* Fixed syncing group topics. To get topics into existing portals on Matrix,
  you can use `!wa sync groups`.
* Fixed sticker events on Matrix including a redundant `msgtype` field.
* Disabled file logging in Docker image by default.
  * To enable it, mount a directory for the logs that's writable for the user
    inside the container (1337 by default), then point the bridge at it using
    the `logging` -> `directory` field, and finally set `file_name_format` to
    something non-empty (the default is `{{.Date}}-{{.Index}}.log`).

[#452]: https://github.com/mautrix/whatsapp/pull/452

# v0.2.4 (2022-02-16)

* Added tracking for incoming events from the primary device to warn the user
  if their phone is offline for too long.
* (Re-)Added support for setting group avatar from Matrix.
* Added initial support for re-fetching old media from phone.
* Added support for bridging audio message waveforms in both directions.
* Added support for sending URL previews to WhatsApp (both custom and autogenerated).
* Updated formatter to get Matrix user displayname when converting WhatsApp mentions.
* Fixed some issues with read receipt bridging.
* Fixed `!wa open` not working with new-style group IDs.
* Fixed panic in disappearing message handling code if a portal is deleted with
  messages still inside.
* Fixed disappearing message timer not being stored in post-login history sync.
* Fixed formatting not being parsed in most incoming WhatsApp messages.

# v0.2.3 (2022-01-16)

* Added support for bridging incoming broadcast list messages.
* Added overrides for mime type -> file extension mapping as some OSes have
  very obscure extensions in their mime type database.
* Added support for personal filtering spaces (started by [@HelderFSFerreira] and [@clmnin] in [#413]).
* Added support for multi-contact messages.
* Added support for disappearing messages.
* Fixed avatar remove events from WhatsApp being ignored.
* Fixed the bridge using the wrong Olm session if a client established a new
  one due to corruption.
* Fixed more issues with app state syncing not working (especially related to contacts).

[@HelderFSFerreira]: https://github.com/HelderFSFerreira
[@clmnin]: https://github.com/clmnin
[#413]: https://github.com/mautrix/whatsapp/pull/413

# v0.2.2 (2021-12-16)

**This release includes a fix for a moderate severity security issue that
affects all versions since v0.1.4.** If your bridge allows untrusted users to
run commands in bridged rooms (`user` permission level), you should update the
bridge or demote untrusted users to the `relay` level. If all whitelisted users
are trusted, then you're not affected.

* Added proper thumbnail generation when sending images to WhatsApp.
* Added support for the stable version of [MSC2778].
* Added support for receiving ephemeral events via [MSC2409]
  (previously only possible via double puppeting).
* Added option to mute status broadcast room and move it to low priority by default.
* Added command to search your contact list and joined groups
  (thanks to [@abmantis] in [#387]).
* Added support for automatically re-requesting Megolm keys if they don't
  arrive, and automatically recreating Olm sessions if an incoming message
  fails to decrypt.
* Added support for passing through media dimensions in image/video messages
  from WhatsApp.
* Switched double puppeted messages to use `"fi.mau.double_puppet_source": "mautrix-whatsapp"`
  instead of `"net.maunium.whatsapp.puppet": true` as the indicator.
* Renamed `!wa check-invite` to `!wa resolve-link` and added support for
  WhatsApp business DM links (`https://wa.me/message/...`).
* Improved read receipt handling to mark all unread messages as read on
  WhatsApp instead of only marking the last message as read.
* Updated Docker image to Alpine 3.15.
* Fixed some issues with app state syncing not working
  (especially related to contacts).
* Fixed responding to retry receipts not working correctly.

[MSC2409]: https://github.com/matrix-org/matrix-spec-proposals/pull/2409
[#387]: https://github.com/mautrix/whatsapp/pull/387

# v0.2.1 (2021-11-10)

* Added support for double puppeting from other servers
  (started by [@abmantis] in [#368]).
  * This does not apply to post-login backfilling, as it's not possible to use
    MSC2716's `/batch_send` with users from other servers.
* Added config updater similar to mautrix-python bridges.
* Added support for responding to retry receipts (to automatically resolve
  other devices not being able to decrypt messages from the bridge).
* Added `sync` command to resync contact and group info to Matrix (not to be
  confused with the pre-v0.2.0 `sync` command which also did backfill and other
  such things).
* Added warning when reacting to messages in portal rooms
  (thanks to [@abmantis] in [#373]).
  * Can be disabled with the `reaction_notices` config option.
* Fixed WhatsApp encryption failing if other user reinstalled the app.
  * New identities will now be auto-trusted, and if `identity_change_notices`
    is set to true, a notice about the change will be sent to the private chat
    portal.
* Fixed contact info sync at login racing with portal/puppet creation and
  therefore not being synced properly.
* Fixed read receipts from WhatsApp on iOS that mark multiple messages as read
  not being handled properly.
* Fixed backfilling not working when double puppeting was not enabled at all.
* Fixed portals not being saved on SQLite.
* Fixed relay mode using old name for users after displayname change.
* Fixed displayname not being HTML-escaped in relay mode message templates.

[@abmantis]: https://github.com/abmantis
[#368]: https://github.com/mautrix/whatsapp/pull/368
[#373]: https://github.com/mautrix/whatsapp/pull/373

# v0.2.0 (2021-11-05)

**N.B.** The minimum Go version is now 1.17. Also note that Docker image paths
have changed as mentioned in the [v0.1.8](#v018-2021-08-07) release notes.

* Switched to WhatsApp multidevice API. All users will have to log into the
  bridge again.
* Initial backfilling now uses [MSC2716]'s batch send endpoint.
  * MSC2716 support is behind a feature flag in Synapse, so initial backfilling
    is disabled by default. See the `history_sync` section in the example
    config for more details.
  * Missed message backfilling (e.g. after bridge downtime) still sends the
    messages normally and is always enabled.
* Replaced old relaybot system with a portal-specific relay user option like in mautrix-signal.
  * You will have to re-setup the relaybot with the new system
    (see [docs](https://docs.mau.fi/bridges/general/relay-mode.html)).
* Many config fields have been changed/renamed/removed, so it is recommended to
  look through the example config and update your config.

[MSC2716]: https://github.com/matrix-org/matrix-spec-proposals/pull/2716

# v0.1.10 (2021-11-02)

This release just disables SQLite foreign keys, since some people already have
invalid rows in the database, which caused issues when SQLite re-checked the
rows during database migrations (#360). The invalid rows shouldn't cause any
actual issues, since the bridge uses foreign keys primarily for cascade delete
purposes.

If you're using Postgres, this update is not necessary.

# v0.1.9 (2021-10-28)

This is the final release targeting the legacy WhatsApp web API that requires a
phone connection. v0.2.0 will switch to the new multidevice API.

* Added support for bridging ephemeral and view-once messages.
* Added custom flag to invite events that will be auto-accepted using double
  puppeting.
* Added proper error message when trying to log in with multidevice enabled.
* Added automatic conversion of webp images to png when sending to WhatsApp
  (thanks to [@apmechev] in [#346]).
* Added support for customizing bridge bot welcome message
  (thanks to [@justinbot] and [@Half-Shot] in [#355]).
* Fixed MetricsHandler causing panics on heavy traffic instances
  (thanks to [@tadzik] in [#359]).
* Removed message content from database.

[@apmechev]: https://github.com/apmechev
[@justinbot]: https://github.com/justinbot
[@Half-Shot]: https://github.com/Half-Shot
[@tadzik]: https://github.com/tadzik
[#346]: https://github.com/mautrix/whatsapp/pull/346
[#355]: https://github.com/mautrix/whatsapp/pull/355
[#359]: https://github.com/mautrix/whatsapp/pull/359

# v0.1.8 (2021-08-07)

**N.B.** Docker images have moved from `dock.mau.dev/tulir/mautrix-whatsapp`
to `dock.mau.dev/mautrix/whatsapp`. New versions are only available at the new
path.

* Added very basic support for bridging [MSC3245] voice messages into
  push-to-talk messages on WhatsApp.
* Added support for Matrix->WhatsApp location messages.
* Renamed `whatsapp_message_age` and `whatsapp_message` prometheus metrics to
  `remote_event_age` and `remote_event` respectively.
* Fixed handling sticker gifs from Matrix.
* Fixed bridging audio/video duration from/to WhatsApp.
* Fixed messages not going through until restart if initial room creation
  attempt fails.
* Fixed issues where some WhatsApp protocol message in new chats prevented the
  first actual message from being bridged.
* Fixed some media from WhatsApp not being bridged due to file length or
  checksum mismatches. WhatsApp clients don't seem to care, so the bridge also
  ignores those errors now.

[MSC3245]: https://github.com/matrix-org/matrix-spec-proposals/pull/3245

# v0.1.7 (2021-06-15)

* Added option to disable creating WhatsApp status broadcast rooms.
* Added option to bridge chat archive, pin and mute statuses from WhatsApp.
* Moved Matrix HTTP request retrying to mautrix-go (which now retries all
  requests instead of only send message requests).
* Made bridge status reporting more generic (now takes an arbitrary HTTP
  endpoint to push bridge status to instead of requiring mautrix-asmux).
* Updated error messages sent to Matrix to be more understandable if WhatsApp
  returns status code 400 or 599.
* Fixed encryption getting messed up after receiving inbound olm sessions if
  using SQLite.
* Fixed bridge sending old messages after new ones if initial backfill limit is
  low and bridge gets restarted.
* Fixed read receipt bridging sometimes marking too many messages as read on
  Matrix (and then echoing it back to WhatsApp).
* Fixed false-positive message send error that showed up on WhatsApp mobile for
  messages sent from Matrix.
* Fixed ghost user displaynames for newly added group members never getting set
  if `chat_meta_sync` is `false`.

# v0.1.6 (2021-04-01)

* Added support for broadcast lists.
* Added automatic re-login-matrix using login shared secret if `/sync` returns `M_UNKNOWN_TOKEN`.
* Added syncing of contact info when syncing room members to ensure that
  WhatsApp ghost users have displaynames before the Matrix user sees them for
  the first time.
* Added bridging of own WhatsApp read receipts after backfilling.
* Added option not to re-sync chat info and user avatars on startup to avoid
  WhatsApp rate limits (error 599).
  * When resync is disabled, chat info changes will still come through by
    backfilling messages. However, user avatars will currently not update after
    being synced once.
* Improved automatic reconnection to work more like WhatsApp Web.
  * The bridge no longer disconnects the websocket if the phone stops
    responding. Instead it sends periodic pings until the phone responds.
  * Outgoing messages will be queued and resent automatically when the phone
    responds again.
* Added option to disable bridging messages where the `msgtype` is `m.notice`
  (thanks to [@hramirezf] in [#259]).
* Fixed backfilling failing in some cases due to 404 errors.
* Merged the whatsapp-ext module into [go-whatsapp].
* Disabled personal filtering communities by default.
* Updated Docker image to Alpine 3.13.

[@hramirezf]: https://github.com/hramirezf
[#259]: https://github.com/mautrix/whatsapp/pull/259
[go-whatsapp]: https://github.com/tulir/go-whatsapp

# v0.1.5 (2020-12-28)

* Renamed device name fields in config from `device_name` and `short_name` to
  `os_name` and `browser_name` respectively.
* Replaced shared secret login with appservice login ([MSC2778]) when logging
  into bridge bot for e2be.
* Removed webp conversion for WhatsApp→Matrix stickers.
* Added short wait if encrypted message arrives before decryption keys.
* Added bridge error notices if messages fail to decrypt.
* Added command to discard the bridge's Megolm session in a room.
* Added retrying for Matrix message sending if server returns 502.
* Added browser-compatible authentication to login API websocket.
* Fixed creating new WhatsApp groups for unencrypted Matrix rooms.
* Changed provisioning API to automatically delete session if logout request fails.
* Changed CI to statically compile olm into the bridge executable.
* Fixed bridging changes to group read-only status to Matrix (thanks to [@rreuvekamp] in [#232]).
* Fixed bridging names of files that were sent from another bridge.
* Fixed handling empty commands.

[MSC2778]: https://github.com/matrix-org/matrix-spec-proposals/pull/2778
[@rreuvekamp]: https://github.com/rreuvekamp
[#232]: https://github.com/mautrix/whatsapp/pull/232

# v0.1.4 (2020-09-04)

* Added better error reporting for media bridging errors.
* Added bridging for own read receipts from WhatsApp mobile when using double
  puppeting.
* Added build tag to disable crypto without disabling SQLite.
* Added support for automatic key sharing.
* Added option to update `m.direct` when using double puppeting.
* Made read receipt bridging toggleable separately from presence bridging.
* Fixed the formatter bridging all numbers starting with `@` on WhatsApp into
  pills on Matrix (now it only bridges actual mentions into pills).
* Fixed handling new contacts and receiving names of non-contacts in groups
  when they send a message.

# v0.1.3 (2020-07-10)

* Added command to create WhatsApp groups.
* Added command to disable bridging presence and read receipts.
* Added full group member syncing (including kicking of users who left before).
* Allowed creating private chat portal by inviting WhatsApp puppet.
* Fixed bug where inaccessible private chat portals couldn't be recreated with
  `pm` command.

# v0.1.2 (2020-07-04)

* Added option to disable notifications during initial backfill.
* Added bridging of contact and location messages.
* Added support for leaving chats and kicking/inviting WhatsApp users from Matrix.
* Added bridging of leaves/kicks/invites from WhatsApp to Matrix.
* Added config option to re-send bridge info state event to all existing portals.
* Added basic prometheus metrics.
* Added low phone battery warning messages.
* Added command to join groups with invite link.
* Fixed media not being encrypted when sending to encrypted portal rooms.

# v0.1.1 (2020-06-04)

* Updated mautrix-go to fix new OTK generation for end-to-bridge encryption.
* Added missing `v` to version command output.
* Fixed creating docker tags for releases.

# v0.1.0 (2020-06-03)

Initial release.
