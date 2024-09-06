package main

import (
	_ "embed"

	up "go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
)

const legacyMigrateRenameTables = `
ALTER TABLE backfill_queue RENAME TO backfill_queue_old;
ALTER TABLE backfill_state RENAME TO backfill_state_old;
ALTER TABLE disappearing_message RENAME TO disappearing_message_old;
ALTER TABLE history_sync_message RENAME TO history_sync_message_old;
ALTER TABLE history_sync_conversation RENAME TO history_sync_conversation_old;
ALTER TABLE media_backfill_requests RENAME TO media_backfill_requests_old;
ALTER TABLE poll_option_id RENAME TO poll_option_id_old;
ALTER TABLE user_portal RENAME TO user_portal_old;
ALTER TABLE portal RENAME TO portal_old;
ALTER TABLE puppet RENAME TO puppet_old;
ALTER TABLE message RENAME TO message_old;
ALTER TABLE reaction RENAME TO reaction_old;
ALTER TABLE "user" RENAME TO user_old;
`

//go:embed legacymigrate.sql
var legacyMigrateCopyData string

func migrateLegacyConfig(helper up.Helper) {
	helper.Set(up.Str, "maunium.net/go/mautrix-whatsapp", "encryption", "pickle_key")
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "displayname_template"}, []string{"network", "displayname_template"})
	// TODO other fields
}
