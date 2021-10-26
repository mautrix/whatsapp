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

	DeliveryReceipts      bool `yaml:"delivery_receipts"`
	MaxConnectionAttempts int  `yaml:"max_connection_attempts"`
	ConnectionRetryDelay  int  `yaml:"connection_retry_delay"`
	ReportConnectionRetry bool `yaml:"report_connection_retry"`
	PortalMessageBuffer   int  `yaml:"portal_message_buffer"`

	CallNotices struct {
		Start bool `yaml:"start"`
		End   bool `yaml:"end"`
	} `yaml:"call_notices"`

	InitialChatSync      int   `yaml:"initial_chat_sync_count"`
	InitialHistoryFill   int   `yaml:"initial_history_fill_count"`
	HistoryDisableNotifs bool  `yaml:"initial_history_disable_notifications"`
	RecoverChatSync      int   `yaml:"recovery_chat_sync_count"`
	RecoverHistory       bool  `yaml:"recovery_history_backfill"`
	ChatMetaSync         bool  `yaml:"chat_meta_sync"`
	UserAvatarSync       bool  `yaml:"user_avatar_sync"`
	BridgeMatrixLeave    bool  `yaml:"bridge_matrix_leave"`
	SyncChatMaxAge       int64 `yaml:"sync_max_chat_age"`

	SyncWithCustomPuppets bool   `yaml:"sync_with_custom_puppets"`
	SyncDirectChatList    bool   `yaml:"sync_direct_chat_list"`
	DefaultBridgeReceipts bool   `yaml:"default_bridge_receipts"`
	DefaultBridgePresence bool   `yaml:"default_bridge_presence"`
	LoginSharedSecret     string `yaml:"login_shared_secret"`

	DoublePuppetBackfill  bool   `yaml:"double_puppet_backfill"`
	PrivateChatPortalMeta bool   `yaml:"private_chat_portal_meta"`
	BridgeNotices         bool   `yaml:"bridge_notices"`
	ResendBridgeInfo      bool   `yaml:"resend_bridge_info"`
	MuteBridging          bool   `yaml:"mute_bridging"`
	ArchiveTag            string `yaml:"archive_tag"`
	PinnedTag             string `yaml:"pinned_tag"`
	TagOnlyOnCreate       bool   `yaml:"tag_only_on_create"`
	MarkReadOnlyOnCreate  bool   `yaml:"mark_read_only_on_create"`
	EnableStatusBroadcast bool   `yaml:"enable_status_broadcast"`

	WhatsappThumbnail bool `yaml:"whatsapp_thumbnail"`

	AllowUserInvite bool `yaml:"allow_user_invite"`

	CommandPrefix string `yaml:"command_prefix"`

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

	Relaybot RelaybotConfig `yaml:"relaybot"`

	usernameTemplate    *template.Template `yaml:"-"`
	displaynameTemplate *template.Template `yaml:"-"`
}

func (bc *BridgeConfig) setDefaults() {
	bc.DeliveryReceipts = false
	bc.MaxConnectionAttempts = 3
	bc.ConnectionRetryDelay = -1
	bc.ReportConnectionRetry = true
	bc.PortalMessageBuffer = 128

	bc.CallNotices.Start = true
	bc.CallNotices.End = true

	bc.InitialChatSync = 10
	bc.InitialHistoryFill = 20
	bc.RecoverChatSync = -1
	bc.RecoverHistory = true
	bc.ChatMetaSync = true
	bc.UserAvatarSync = true
	bc.BridgeMatrixLeave = true
	bc.SyncChatMaxAge = 259200

	bc.SyncWithCustomPuppets = true
	bc.DefaultBridgePresence = true
	bc.DefaultBridgeReceipts = true
	bc.LoginSharedSecret = ""

	bc.DoublePuppetBackfill = false
	bc.PrivateChatPortalMeta = false
	bc.BridgeNotices = true
	bc.EnableStatusBroadcast = true
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
	PermissionLevelDefault  PermissionLevel = 0
	PermissionLevelRelaybot PermissionLevel = 5
	PermissionLevelUser     PermissionLevel = 10
	PermissionLevelAdmin    PermissionLevel = 100
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
		case "relaybot":
			(*pc)[key] = PermissionLevelRelaybot
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
		case PermissionLevelRelaybot:
			rawPC[key] = "relaybot"
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

func (pc PermissionConfig) IsRelaybotWhitelisted(userID id.UserID) bool {
	return pc.GetPermissionLevel(userID) >= PermissionLevelRelaybot
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
	Enabled        bool        `yaml:"enabled"`
	ManagementRoom id.RoomID   `yaml:"management"`
	InviteUsers    []id.UserID `yaml:"invites"`

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
	UserID id.UserID
	*event.MemberEventContent
}

type formatData struct {
	Sender  Sender
	Message string
	Content *event.MessageEventContent
}

func (rc *RelaybotConfig) FormatMessage(content *event.MessageEventContent, sender id.UserID, member *event.MemberEventContent) (string, error) {
	var output strings.Builder
	err := rc.messageTemplates.ExecuteTemplate(&output, string(content.MsgType), formatData{
		Sender: Sender{
			UserID:             sender,
			MemberEventContent: member,
		},
		Content: content,
		Message: content.FormattedBody,
	})
	return output.String(), err
}
