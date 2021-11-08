// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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
	"fmt"
	"os"
	"path"

	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/appservice"
)

func (helper *UpgradeHelper) TempMethod() {
	helper.doUpgrade()
}

func (helper *UpgradeHelper) doUpgrade() {
	helper.Copy(Str, "homeserver", "address")
	helper.Copy(Str, "homeserver", "domain")
	helper.Copy(Bool, "homeserver", "asmux")
	helper.Copy(Str|Null, "homeserver", "status_endpoint")

	helper.Copy(Str, "appservice", "address")
	helper.Copy(Str, "appservice", "hostname")
	helper.Copy(Int, "appservice", "port")
	helper.Copy(Str, "appservice", "database", "type")
	helper.Copy(Str, "appservice", "database", "uri")
	helper.Copy(Int, "appservice", "database", "max_open_conns")
	helper.Copy(Int, "appservice", "database", "max_idle_conns")
	helper.Copy(Str, "appservice", "provisioning", "prefix")
	if secret, ok := helper.Get(Str, "appservice", "provisioning", "shared_secret"); !ok || secret == "generate" {
		sharedSecret := appservice.RandomString(64)
		helper.Set(Str, sharedSecret, "appservice", "provisioning", "shared_secret")
	} else {
		helper.Copy(Str, "appservice", "provisioning", "shared_secret")
	}
	helper.Copy(Str, "appservice", "id")
	helper.Copy(Str, "appservice", "bot", "username")
	helper.Copy(Str, "appservice", "bot", "displayname")
	helper.Copy(Str, "appservice", "bot", "avatar")
	helper.Copy(Str, "appservice", "as_token")
	helper.Copy(Str, "appservice", "hs_token")

	helper.Copy(Bool, "metrics", "enabled")
	helper.Copy(Str, "metrics", "listen")

	helper.Copy(Str, "whatsapp", "os_name")
	helper.Copy(Str, "whatsapp", "browser_name")

	helper.Copy(Str, "bridge", "username_template")
	helper.Copy(Str, "bridge", "displayname_template")
	helper.Copy(Bool, "bridge", "delivery_receipts")
	helper.Copy(Int, "bridge", "portal_message_buffer")
	helper.Copy(Bool, "bridge", "call_start_notices")
	helper.Copy(Bool, "bridge", "history_sync", "create_portals")
	helper.Copy(Int, "bridge", "history_sync", "max_age")
	helper.Copy(Bool, "bridge", "history_sync", "backfill")
	helper.Copy(Bool, "bridge", "history_sync", "double_puppet_backfill")
	helper.Copy(Bool, "bridge", "history_sync", "request_full_sync")
	helper.Copy(Bool, "bridge", "user_avatar_sync")
	helper.Copy(Bool, "bridge", "bridge_matrix_leave")
	helper.Copy(Bool, "bridge", "sync_with_custom_puppets")
	helper.Copy(Bool, "bridge", "sync_direct_chat_list")
	helper.Copy(Bool, "bridge", "default_bridge_receipts")
	helper.Copy(Bool, "bridge", "default_bridge_presence")
	helper.Copy(Map, "bridge", "double_puppet_server_map")
	helper.Copy(Bool, "bridge", "double_puppet_allow_discovery")
	if legacySecret, ok := helper.Get(Str, "bridge", "login_shared_secret"); ok && len(legacySecret) > 0 {
		baseNode := helper.GetBaseNode("bridge", "login_shared_secret_map")
		baseNode.Map[helper.GetBase("homeserver", "domain")] = YAMLNode{Node: makeStringNode(legacySecret)}
		baseNode.Content = baseNode.Map.toNodes()
	} else {
		helper.Copy(Map, "bridge", "login_shared_secret_map")
	}
	helper.Copy(Bool, "bridge", "private_chat_portal_meta")
	helper.Copy(Bool, "bridge", "bridge_notices")
	helper.Copy(Bool, "bridge", "resend_bridge_info")
	helper.Copy(Bool, "bridge", "mute_bridging")
	helper.Copy(Str|Null, "bridge", "archive_tag")
	helper.Copy(Str|Null, "bridge", "pinned_tag")
	helper.Copy(Bool, "bridge", "tag_only_on_create")
	helper.Copy(Bool, "bridge", "enable_status_broadcast")
	helper.Copy(Bool, "bridge", "whatsapp_thumbnail")
	helper.Copy(Bool, "bridge", "allow_user_invite")
	helper.Copy(Str, "bridge", "command_prefix")
	helper.Copy(Bool, "bridge", "federate_rooms")
	helper.Copy(Str, "bridge", "management_room_text", "welcome")
	helper.Copy(Str, "bridge", "management_room_text", "welcome_connected")
	helper.Copy(Str, "bridge", "management_room_text", "welcome_unconnected")
	helper.Copy(Str|Null, "bridge", "management_room_text", "additional_help")
	helper.Copy(Bool, "bridge", "encryption", "allow")
	helper.Copy(Bool, "bridge", "encryption", "default")
	helper.Copy(Bool, "bridge", "encryption", "key_sharing", "allow")
	helper.Copy(Bool, "bridge", "encryption", "key_sharing", "require_cross_signing")
	helper.Copy(Bool, "bridge", "encryption", "key_sharing", "require_verification")
	helper.Copy(Map, "bridge", "permissions")
	helper.Copy(Bool, "bridge", "relay", "enabled")
	helper.Copy(Bool, "bridge", "relay", "admin_only")
	helper.Copy(Map, "bridge", "relay", "message_formats")

	helper.Copy(Str, "logging", "directory")
	helper.Copy(Str|Null, "logging", "file_name_format")
	helper.Copy(Str|Timestamp, "logging", "file_date_format")
	helper.Copy(Int, "logging", "file_mode")
	helper.Copy(Str|Timestamp, "logging", "timestamp_format")
	helper.Copy(Str, "logging", "print_level")
}

func Mutate(path string, mutate func(helper *UpgradeHelper)) error {
	_, _, err := upgrade(path, true, mutate)
	return err
}

func Upgrade(path string, save bool) ([]byte, bool, error) {
	return upgrade(path, save, nil)
}

func upgrade(configPath string, save bool, mutate func(helper *UpgradeHelper)) ([]byte, bool, error) {
	sourceData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read config: %w", err)
	}
	var base, cfg yaml.Node
	err = yaml.Unmarshal([]byte(ExampleConfig), &base)
	if err != nil {
		return sourceData, false, fmt.Errorf("failed to unmarshal example config: %w", err)
	}
	err = yaml.Unmarshal(sourceData, &cfg)
	if err != nil {
		return sourceData, false, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	helper := NewUpgradeHelper(&base, &cfg)
	helper.doUpgrade()
	if mutate != nil {
		mutate(helper)
	}

	output, err := yaml.Marshal(&base)
	if err != nil {
		return sourceData, false, fmt.Errorf("failed to marshal updated config: %w", err)
	}
	if save {
		var tempFile *os.File
		tempFile, err = os.CreateTemp(path.Dir(configPath), "wa-config-*.yaml")
		if err != nil {
			return output, true, fmt.Errorf("failed to create temp file for writing config: %w", err)
		}
		_, err = tempFile.Write(output)
		if err != nil {
			_ = os.Remove(tempFile.Name())
			return output, true, fmt.Errorf("failed to write updated config to temp file: %w", err)
		}
		err = os.Rename(tempFile.Name(), configPath)
		if err != nil {
			_ = os.Remove(tempFile.Name())
			return output, true, fmt.Errorf("failed to override current config with temp file: %w", err)
		}
	}
	return output, true, nil
}
