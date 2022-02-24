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
	"strconv"
	"strings"
	"text/template"

	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type BridgeConfig struct {
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`

	PersonalFilteringSpaces bool `yaml:"personal_filtering_spaces"`

	DeliveryReceipts      bool `yaml:"delivery_receipts"`
	PortalMessageBuffer   int  `yaml:"portal_message_buffer"`
	CallStartNotices      bool `yaml:"call_start_notices"`
	IdentityChangeNotices bool `yaml:"identity_change_notices"`
	ReactionNotices       bool `yaml:"reaction_notices"`

	HistorySync struct {
		CreatePortals        bool  `yaml:"create_portals"`
		MaxAge               int64 `yaml:"max_age"`
		Backfill             bool  `yaml:"backfill"`
		DoublePuppetBackfill bool  `yaml:"double_puppet_backfill"`
		RequestFullSync      bool  `yaml:"request_full_sync"`
	} `yaml:"history_sync"`
	UserAvatarSync    bool `yaml:"user_avatar_sync"`
	BridgeMatrixLeave bool `yaml:"bridge_matrix_leave"`

	SyncWithCustomPuppets bool `yaml:"sync_with_custom_puppets"`
	SyncDirectChatList    bool `yaml:"sync_direct_chat_list"`
	DefaultBridgeReceipts bool `yaml:"default_bridge_receipts"`
	DefaultBridgePresence bool `yaml:"default_bridge_presence"`

	ForceActiveDeliveryReceipts bool `yaml:"force_active_delivery_receipts"`

	DoublePuppetServerMap      map[string]string `yaml:"double_puppet_server_map"`
	DoublePuppetAllowDiscovery bool              `yaml:"double_puppet_allow_discovery"`
	LoginSharedSecretMap       map[string]string `yaml:"login_shared_secret_map"`

	PrivateChatPortalMeta bool   `yaml:"private_chat_portal_meta"`
	BridgeNotices         bool   `yaml:"bridge_notices"`
	ResendBridgeInfo      bool   `yaml:"resend_bridge_info"`
	MuteBridging          bool   `yaml:"mute_bridging"`
	ArchiveTag            string `yaml:"archive_tag"`
	PinnedTag             string `yaml:"pinned_tag"`
	TagOnlyOnCreate       bool   `yaml:"tag_only_on_create"`
	MarkReadOnlyOnCreate  bool   `yaml:"mark_read_only_on_create"`
	EnableStatusBroadcast bool   `yaml:"enable_status_broadcast"`
	MuteStatusBroadcast   bool   `yaml:"mute_status_broadcast"`
	WhatsappThumbnail     bool   `yaml:"whatsapp_thumbnail"`
	AllowUserInvite       bool   `yaml:"allow_user_invite"`
	FederateRooms         bool   `yaml:"federate_rooms"`
	URLPreviews           bool   `yaml:"url_previews"`

	DisappearingMessagesInGroups bool `yaml:"disappearing_messages_in_groups"`

	DisableBridgeAlerts bool `yaml:"disable_bridge_alerts"`

	CommandPrefix string `yaml:"command_prefix"`

	ManagementRoomText struct {
		Welcome            string `yaml:"welcome"`
		WelcomeConnected   string `yaml:"welcome_connected"`
		WelcomeUnconnected string `yaml:"welcome_unconnected"`
		AdditionalHelp     string `yaml:"additional_help"`
	} `yaml:"management_room_text"`

	Encryption struct {
		Allow   bool `yaml:"allow"`
		Default bool `yaml:"default"`

		KeySharing struct {
			Allow               bool `yaml:"allow"`
			RequireCrossSigning bool `yaml:"require_cross_signing"`
			RequireVerification bool `yaml:"require_verification"`
		} `yaml:"key_sharing"`
	} `yaml:"encryption"`

	Permissions PermissionConfig `yaml:"permissions"`

	Relay RelaybotConfig `yaml:"relay"`

	usernameTemplate    *template.Template `yaml:"-"`
	displaynameTemplate *template.Template `yaml:"-"`
}

type umBridgeConfig BridgeConfig

func (bc *BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umBridgeConfig)(bc))
	if err != nil {
		return err
	}

	bc.usernameTemplate, err = template.New("username").Parse(bc.UsernameTemplate)
	if err != nil {
		return err
	} else if !strings.Contains(bc.FormatUsername("1234567890"), "1234567890") {
		return fmt.Errorf("username template is missing user ID placeholder")
	}

	bc.displaynameTemplate, err = template.New("displayname").Parse(bc.DisplaynameTemplate)
	if err != nil {
		return err
	}

	return nil
}

type UsernameTemplateArgs struct {
	UserID id.UserID
}

type legacyContactInfo struct {
	types.ContactInfo
	Phone string

	Notify string
	VName  string
	Name   string
	Short  string
	JID    string
}

func (bc BridgeConfig) FormatDisplayname(jid types.JID, contact types.ContactInfo) (string, int8) {
	var buf strings.Builder
	_ = bc.displaynameTemplate.Execute(&buf, legacyContactInfo{
		ContactInfo: contact,
		Notify:      contact.PushName,
		VName:       contact.BusinessName,
		Name:        contact.FullName,
		Short:       contact.FirstName,
		Phone:       "+" + jid.User,
		JID:         "+" + jid.User,
	})
	var quality int8
	switch {
	case len(contact.PushName) > 0 || len(contact.BusinessName) > 0:
		quality = 3
	case len(contact.FullName) > 0 || len(contact.FirstName) > 0:
		quality = 2
	default:
		quality = 1
	}
	return buf.String(), quality
}

func (bc BridgeConfig) FormatUsername(username string) string {
	var buf strings.Builder
	_ = bc.usernameTemplate.Execute(&buf, username)
	return buf.String()
}

type PermissionConfig map[string]PermissionLevel

type PermissionLevel int

const (
	PermissionLevelDefault PermissionLevel = 0
	PermissionLevelRelay   PermissionLevel = 5
	PermissionLevelUser    PermissionLevel = 10
	PermissionLevelAdmin   PermissionLevel = 100
)

func (pc *PermissionConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	rawPC := make(map[string]string)
	err := unmarshal(&rawPC)
	if err != nil {
		return err
	}

	if *pc == nil {
		*pc = make(map[string]PermissionLevel)
	}
	for key, value := range rawPC {
		switch strings.ToLower(value) {
		case "relaybot", "relay":
			(*pc)[key] = PermissionLevelRelay
		case "user":
			(*pc)[key] = PermissionLevelUser
		case "admin":
			(*pc)[key] = PermissionLevelAdmin
		default:
			val, err := strconv.Atoi(value)
			if err != nil {
				(*pc)[key] = PermissionLevelDefault
			} else {
				(*pc)[key] = PermissionLevel(val)
			}
		}
	}
	return nil
}

func (pc *PermissionConfig) MarshalYAML() (interface{}, error) {
	if *pc == nil {
		return nil, nil
	}
	rawPC := make(map[string]string)
	for key, value := range *pc {
		switch value {
		case PermissionLevelRelay:
			rawPC[key] = "relay"
		case PermissionLevelUser:
			rawPC[key] = "user"
		case PermissionLevelAdmin:
			rawPC[key] = "admin"
		default:
			rawPC[key] = strconv.Itoa(int(value))
		}
	}
	return rawPC, nil
}

func (pc PermissionConfig) IsRelayWhitelisted(userID id.UserID) bool {
	return pc.GetPermissionLevel(userID) >= PermissionLevelRelay
}

func (pc PermissionConfig) IsWhitelisted(userID id.UserID) bool {
	return pc.GetPermissionLevel(userID) >= PermissionLevelUser
}

func (pc PermissionConfig) IsAdmin(userID id.UserID) bool {
	return pc.GetPermissionLevel(userID) >= PermissionLevelAdmin
}

func (pc PermissionConfig) GetPermissionLevel(userID id.UserID) PermissionLevel {
	permissions, ok := pc[string(userID)]
	if ok {
		return permissions
	}

	_, homeserver, _ := userID.Parse()
	permissions, ok = pc[homeserver]
	if len(homeserver) > 0 && ok {
		return permissions
	}

	permissions, ok = pc["*"]
	if ok {
		return permissions
	}

	return PermissionLevelDefault
}

type RelaybotConfig struct {
	Enabled          bool                         `yaml:"enabled"`
	AdminOnly        bool                         `yaml:"admin_only"`
	MessageFormats   map[event.MessageType]string `yaml:"message_formats"`
	messageTemplates *template.Template           `yaml:"-"`
}

type umRelaybotConfig RelaybotConfig

func (rc *RelaybotConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umRelaybotConfig)(rc))
	if err != nil {
		return err
	}

	rc.messageTemplates = template.New("messageTemplates")
	for key, format := range rc.MessageFormats {
		_, err := rc.messageTemplates.New(string(key)).Parse(format)
		if err != nil {
			return err
		}
	}

	return nil
}

type Sender struct {
	UserID string
	event.MemberEventContent
}

type formatData struct {
	Sender  Sender
	Message string
	Content *event.MessageEventContent
}

func (rc *RelaybotConfig) FormatMessage(content *event.MessageEventContent, sender id.UserID, member event.MemberEventContent) (string, error) {
	if len(member.Displayname) == 0 {
		member.Displayname = sender.String()
	}
	member.Displayname = template.HTMLEscapeString(member.Displayname)
	var output strings.Builder
	err := rc.messageTemplates.ExecuteTemplate(&output, string(content.MsgType), formatData{
		Sender: Sender{
			UserID:             template.HTMLEscapeString(sender.String()),
			MemberEventContent: member,
		},
		Content: content,
		Message: content.FormattedBody,
	})
	return output.String(), err
}
