// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package config

import (
	"strings"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	up "maunium.net/go/mautrix/util/configupgrade"
)

func DoUpgrade(helper *up.Helper) {
	bridgeconfig.Upgrader.DoUpgrade(helper)

	helper.Copy(up.Str|up.Null, "segment_key")

	helper.Copy(up.Bool, "metrics", "enabled")
	helper.Copy(up.Str, "metrics", "listen")

	helper.Copy(up.Str, "whatsapp", "os_name")
	helper.Copy(up.Str, "whatsapp", "browser_name")

	helper.Copy(up.Str, "bridge", "username_template")
	helper.Copy(up.Str, "bridge", "displayname_template")
	helper.Copy(up.Bool, "bridge", "personal_filtering_spaces")
	helper.Copy(up.Bool, "bridge", "delivery_receipts")
	helper.Copy(up.Bool, "bridge", "message_status_events")
	helper.Copy(up.Bool, "bridge", "message_error_notices")
	helper.Copy(up.Int, "bridge", "portal_message_buffer")
	helper.Copy(up.Bool, "bridge", "call_start_notices")
	helper.Copy(up.Bool, "bridge", "identity_change_notices")
	helper.Copy(up.Bool, "bridge", "history_sync", "create_portals")
	helper.Copy(up.Bool, "bridge", "history_sync", "backfill")
	helper.Copy(up.Bool, "bridge", "history_sync", "double_puppet_backfill")
	helper.Copy(up.Bool, "bridge", "history_sync", "request_full_sync")
	helper.Copy(up.Bool, "bridge", "history_sync", "media_requests", "auto_request_media")
	helper.Copy(up.Str, "bridge", "history_sync", "media_requests", "request_method")
	helper.Copy(up.Int, "bridge", "history_sync", "media_requests", "request_local_time")
	helper.Copy(up.Int, "bridge", "history_sync", "max_initial_conversations")
	helper.Copy(up.Int, "bridge", "history_sync", "immediate", "worker_count")
	helper.Copy(up.Int, "bridge", "history_sync", "immediate", "max_events")
	helper.Copy(up.List, "bridge", "history_sync", "deferred")
	helper.Copy(up.Bool, "bridge", "user_avatar_sync")
	helper.Copy(up.Bool, "bridge", "bridge_matrix_leave")
	helper.Copy(up.Bool, "bridge", "sync_with_custom_puppets")
	helper.Copy(up.Bool, "bridge", "sync_direct_chat_list")
	helper.Copy(up.Bool, "bridge", "default_bridge_receipts")
	helper.Copy(up.Bool, "bridge", "default_bridge_presence")
	helper.Copy(up.Bool, "bridge", "send_presence_on_typing")
	helper.Copy(up.Bool, "bridge", "force_active_delivery_receipts")
	helper.Copy(up.Map, "bridge", "double_puppet_server_map")
	helper.Copy(up.Bool, "bridge", "double_puppet_allow_discovery")
	if legacySecret, ok := helper.Get(up.Str, "bridge", "login_shared_secret"); ok && len(legacySecret) > 0 {
		baseNode := helper.GetBaseNode("bridge", "login_shared_secret_map")
		baseNode.Map[helper.GetBase("homeserver", "domain")] = up.StringNode(legacySecret)
		baseNode.UpdateContent()
	} else {
		helper.Copy(up.Map, "bridge", "login_shared_secret_map")
	}
	helper.Copy(up.Bool, "bridge", "private_chat_portal_meta")
	helper.Copy(up.Bool, "bridge", "bridge_notices")
	helper.Copy(up.Bool, "bridge", "resend_bridge_info")
	helper.Copy(up.Bool, "bridge", "mute_bridging")
	helper.Copy(up.Str|up.Null, "bridge", "archive_tag")
	helper.Copy(up.Str|up.Null, "bridge", "pinned_tag")
	helper.Copy(up.Bool, "bridge", "tag_only_on_create")
	helper.Copy(up.Bool, "bridge", "enable_status_broadcast")
	helper.Copy(up.Bool, "bridge", "disable_status_broadcast_send")
	helper.Copy(up.Bool, "bridge", "mute_status_broadcast")
	helper.Copy(up.Str|up.Null, "bridge", "status_broadcast_tag")
	helper.Copy(up.Bool, "bridge", "whatsapp_thumbnail")
	helper.Copy(up.Bool, "bridge", "allow_user_invite")
	helper.Copy(up.Str, "bridge", "command_prefix")
	helper.Copy(up.Bool, "bridge", "federate_rooms")
	helper.Copy(up.Bool, "bridge", "disappearing_messages_in_groups")
	helper.Copy(up.Bool, "bridge", "disable_bridge_alerts")
	helper.Copy(up.Bool, "bridge", "url_previews")
	helper.Copy(up.Str, "bridge", "management_room_text", "welcome")
	helper.Copy(up.Str, "bridge", "management_room_text", "welcome_connected")
	helper.Copy(up.Str, "bridge", "management_room_text", "welcome_unconnected")
	helper.Copy(up.Str|up.Null, "bridge", "management_room_text", "additional_help")
	helper.Copy(up.Bool, "bridge", "encryption", "allow")
	helper.Copy(up.Bool, "bridge", "encryption", "default")
	helper.Copy(up.Bool, "bridge", "encryption", "key_sharing", "allow")
	helper.Copy(up.Bool, "bridge", "encryption", "key_sharing", "require_cross_signing")
	helper.Copy(up.Bool, "bridge", "encryption", "key_sharing", "require_verification")
	if prefix, ok := helper.Get(up.Str, "appservice", "provisioning", "prefix"); ok {
		helper.Set(up.Str, strings.TrimSuffix(prefix, "/v1"), "bridge", "provisioning", "prefix")
	} else {
		helper.Copy(up.Str, "bridge", "provisioning", "prefix")
	}
	if secret, ok := helper.Get(up.Str, "appservice", "provisioning", "shared_secret"); ok && secret != "generate" {
		helper.Set(up.Str, secret, "bridge", "provisioning", "shared_secret")
	} else if secret, ok = helper.Get(up.Str, "bridge", "provisioning", "shared_secret"); !ok || secret == "generate" {
		sharedSecret := appservice.RandomString(64)
		helper.Set(up.Str, sharedSecret, "bridge", "provisioning", "shared_secret")
	} else {
		helper.Copy(up.Str, "bridge", "provisioning", "shared_secret")
	}
	helper.Copy(up.Map, "bridge", "permissions")
	helper.Copy(up.Bool, "bridge", "relay", "enabled")
	helper.Copy(up.Bool, "bridge", "relay", "admin_only")
	helper.Copy(up.Map, "bridge", "relay", "message_formats")
}

var SpacedBlocks = [][]string{
	{"homeserver", "asmux"},
	{"appservice"},
	{"appservice", "hostname"},
	{"appservice", "database"},
	{"appservice", "id"},
	{"appservice", "as_token"},
	{"segment_key"},
	{"metrics"},
	{"whatsapp"},
	{"bridge"},
	{"bridge", "command_prefix"},
	{"bridge", "management_room_text"},
	{"bridge", "encryption"},
	{"bridge", "provisioning"},
	{"bridge", "permissions"},
	{"bridge", "relay"},
	{"logging"},
}
