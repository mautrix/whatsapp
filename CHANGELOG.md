# v0.2.3 (unreleased)

* Added support for bridging incoming broadcast list messages.
* Added overrides for mime type -> file extension mapping as some OSes have
  very obscure extensions in their mime type database.
* Added support for personal filtering spaces (started by [@HelderFSFerreira] and [@clmnin] in [#413]).
* Added support for multi-contact messages.
* Fixed avatar remove events from WhatsApp being ignored.
* Fixed the bridge using the wrong Olm session if a client established a new
  one due to corruption.

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

[MSC2409]: https://github.com/matrix-org/matrix-doc/pull/2409
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

[MSC2716]: https://github.com/matrix-org/matrix-doc/pull/2716

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

[MSC3245]: https://github.com/matrix-org/matrix-doc/pull/3245

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

[MSC2778]: https://github.com/matrix-org/matrix-doc/pull/2778
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
