package main

import (
	_ "embed"
	"strings"

	up "go.mau.fi/util/configupgrade"
	"go.mau.fi/util/dbutil/litestream"
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

func init() {
	litestream.Functions["split_part"] = func(input, delimiter string, partNum int) string {
		// split_part is 1-indexed
		partNum--
		parts := strings.Split(input, delimiter)
		if len(parts) <= partNum {
			return ""
		}
		return parts[partNum]
	}
}

func migrateLegacyConfig(helper up.Helper) {
	helper.Set(up.Str, "maunium.net/go/mautrix-whatsapp", "encryption", "pickle_key")
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"whatsapp", "os_name"}, []string{"network", "os_name"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"whatsapp", "browser_name"}, []string{"network", "browser_name"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"whatsapp", "proxy"}, []string{"network", "proxy"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"whatsapp", "get_proxy_url"}, []string{"network", "get_proxy_url"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"whatsapp", "proxy_only_login"}, []string{"network", "proxy_only_login"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "displayname_template"}, []string{"network", "displayname_template"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "call_start_notices"}, []string{"network", "call_start_notices"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "send_presence_on_typing"}, []string{"network", "send_presence_on_typing"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "enable_status_broadcast"}, []string{"network", "enable_status_broadcast"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "disable_status_broadcast_send"}, []string{"network", "disable_status_broadcast_send"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "mute_status_broadcast"}, []string{"network", "mute_status_broadcast"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "status_broadcast_tag"}, []string{"network", "status_broadcast_tag"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "whatsapp_thumbnail"}, []string{"network", "whatsapp_thumbnail"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "url_previews"}, []string{"network", "url_previews"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "extev_polls"}, []string{"network", "extev_polls"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "force_active_delivery_receipts"}, []string{"network", "force_active_delivery_receipts"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "history_sync", "max_initial_conversations"}, []string{"network", "history_sync", "max_initial_conversations"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "history_sync", "request_full_sync"}, []string{"network", "history_sync", "request_full_sync"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "history_sync", "full_sync_config", "days_limit"}, []string{"network", "history_sync", "full_sync_config", "days_limit"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "history_sync", "full_sync_config", "size_limit_mb"}, []string{"network", "history_sync", "full_sync_config", "size_limit_mb"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "history_sync", "full_sync_config", "storage_quota_mb"}, []string{"network", "history_sync", "full_sync_config", "storage_quota_mb"})
	bridgeconfig.CopyToOtherLocation(helper, up.Bool, []string{"bridge", "history_sync", "media_requests", "auto_request_media"}, []string{"network", "history_sync", "media_requests", "auto_request_media"})
	bridgeconfig.CopyToOtherLocation(helper, up.Str, []string{"bridge", "history_sync", "media_requests", "request_method"}, []string{"network", "history_sync", "media_requests", "request_method"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "history_sync", "media_requests", "request_local_time"}, []string{"network", "history_sync", "media_requests", "request_local_time"})
	bridgeconfig.CopyToOtherLocation(helper, up.Int, []string{"bridge", "history_sync", "media_requests", "max_async_handle"}, []string{"network", "history_sync", "media_requests", "max_async_handle"})
}
