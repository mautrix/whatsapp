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
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type DeferredConfig struct {
	StartDaysAgo   int `yaml:"start_days_ago"`
	MaxBatchEvents int `yaml:"max_batch_events"`
	BatchDelay     int `yaml:"batch_delay"`
}

type MediaRequestMethod string

const (
	MediaRequestMethodImmediate MediaRequestMethod = "immediate"
	MediaRequestMethodLocalTime                    = "local_time"
)

type BridgeConfig struct {
	UsernameTemplate    string `yaml:"username_template"`
	DisplaynameTemplate string `yaml:"displayname_template"`

	PersonalFilteringSpaces bool `yaml:"personal_filtering_spaces"`

	DeliveryReceipts      bool `yaml:"delivery_receipts"`
	MessageStatusEvents   bool `yaml:"message_status_events"`
	MessageErrorNotices   bool `yaml:"message_error_notices"`
	PortalMessageBuffer   int  `yaml:"portal_message_buffer"`
	CallStartNotices      bool `yaml:"call_start_notices"`
	IdentityChangeNotices bool `yaml:"identity_change_notices"`

	HistorySync struct {
		CreatePortals bool `yaml:"create_portals"`
		Backfill      bool `yaml:"backfill"`

		DoublePuppetBackfill    bool `yaml:"double_puppet_backfill"`
		RequestFullSync         bool `yaml:"request_full_sync"`
		MaxInitialConversations int  `yaml:"max_initial_conversations"`
		UnreadHoursThreshold    int  `yaml:"unread_hours_threshold"`

		Immediate struct {
			WorkerCount int `yaml:"worker_count"`
			MaxEvents   int `yaml:"max_events"`
		} `yaml:"immediate"`

		MediaRequests struct {
			AutoRequestMedia bool               `yaml:"auto_request_media"`
			RequestMethod    MediaRequestMethod `yaml:"request_method"`
			RequestLocalTime int                `yaml:"request_local_time"`
		} `yaml:"media_requests"`

		Deferred []DeferredConfig `yaml:"deferred"`
	} `yaml:"history_sync"`
	UserAvatarSync    bool `yaml:"user_avatar_sync"`
	BridgeMatrixLeave bool `yaml:"bridge_matrix_leave"`

	SyncWithCustomPuppets  bool `yaml:"sync_with_custom_puppets"`
	SyncDirectChatList     bool `yaml:"sync_direct_chat_list"`
	SyncManualMarkedUnread bool `yaml:"sync_manual_marked_unread"`
	DefaultBridgeReceipts  bool `yaml:"default_bridge_receipts"`
	DefaultBridgePresence  bool `yaml:"default_bridge_presence"`
	SendPresenceOnTyping   bool `yaml:"send_presence_on_typing"`

	ForceActiveDeliveryReceipts bool `yaml:"force_active_delivery_receipts"`

	DoublePuppetServerMap      map[string]string `yaml:"double_puppet_server_map"`
	DoublePuppetAllowDiscovery bool              `yaml:"double_puppet_allow_discovery"`
	LoginSharedSecretMap       map[string]string `yaml:"login_shared_secret_map"`

	PrivateChatPortalMeta bool   `yaml:"private_chat_portal_meta"`
	ParallelMemberSync    bool   `yaml:"parallel_member_sync"`
	BridgeNotices         bool   `yaml:"bridge_notices"`
	ResendBridgeInfo      bool   `yaml:"resend_bridge_info"`
	MuteBridging          bool   `yaml:"mute_bridging"`
	ArchiveTag            string `yaml:"archive_tag"`
	PinnedTag             string `yaml:"pinned_tag"`
	TagOnlyOnCreate       bool   `yaml:"tag_only_on_create"`
	MarkReadOnlyOnCreate  bool   `yaml:"mark_read_only_on_create"`
	EnableStatusBroadcast bool   `yaml:"enable_status_broadcast"`
	MuteStatusBroadcast   bool   `yaml:"mute_status_broadcast"`
	StatusBroadcastTag    string `yaml:"status_broadcast_tag"`
	WhatsappThumbnail     bool   `yaml:"whatsapp_thumbnail"`
	AllowUserInvite       bool   `yaml:"allow_user_invite"`
	FederateRooms         bool   `yaml:"federate_rooms"`
	URLPreviews           bool   `yaml:"url_previews"`
	CaptionInMessage      bool   `yaml:"caption_in_message"`
	SendWhatsAppEdits     bool   `yaml:"send_whatsapp_edits"`

	MessageHandlingTimeout struct {
		ErrorAfterStr string `yaml:"error_after"`
		DeadlineStr   string `yaml:"deadline"`

		ErrorAfter time.Duration `yaml:"-"`
		Deadline   time.Duration `yaml:"-"`
	} `yaml:"message_handling_timeout"`

	DisableStatusBroadcastSend   bool `yaml:"disable_status_broadcast_send"`
	DisappearingMessagesInGroups bool `yaml:"disappearing_messages_in_groups"`

	DisableBridgeAlerts   bool `yaml:"disable_bridge_alerts"`
	CrashOnStreamReplaced bool `yaml:"crash_on_stream_replaced"`

	CommandPrefix string `yaml:"command_prefix"`

	ManagementRoomText bridgeconfig.ManagementRoomTexts `yaml:"management_room_text"`

	Encryption bridgeconfig.EncryptionConfig `yaml:"encryption"`

	Provisioning struct {
		Prefix       string `yaml:"prefix"`
		SharedSecret string `yaml:"shared_secret"`
	} `yaml:"provisioning"`

	Permissions bridgeconfig.PermissionConfig `yaml:"permissions"`

	Relay RelaybotConfig `yaml:"relay"`

	ParsedUsernameTemplate *template.Template `yaml:"-"`
	displaynameTemplate    *template.Template `yaml:"-"`
}

func (bc BridgeConfig) GetEncryptionConfig() bridgeconfig.EncryptionConfig {
	return bc.Encryption
}

func (bc BridgeConfig) EnableMessageStatusEvents() bool {
	return bc.MessageStatusEvents
}

func (bc BridgeConfig) EnableMessageErrorNotices() bool {
	return bc.MessageErrorNotices
}

func (bc BridgeConfig) GetCommandPrefix() string {
	return bc.CommandPrefix
}

func (bc BridgeConfig) GetManagementRoomTexts() bridgeconfig.ManagementRoomTexts {
	return bc.ManagementRoomText
}

func (bc BridgeConfig) GetResendBridgeInfo() bool {
	return bc.ResendBridgeInfo
}

func boolToInt(val bool) int {
	if val {
		return 1
	}
	return 0
}

func (bc BridgeConfig) Validate() error {
	_, hasWildcard := bc.Permissions["*"]
	_, hasExampleDomain := bc.Permissions["example.com"]
	_, hasExampleUser := bc.Permissions["@admin:example.com"]
	exampleLen := boolToInt(hasWildcard) + boolToInt(hasExampleUser) + boolToInt(hasExampleDomain)
	if len(bc.Permissions) <= exampleLen {
		return errors.New("bridge.permissions not configured")
	}
	return nil
}

type umBridgeConfig BridgeConfig

func (bc *BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umBridgeConfig)(bc))
	if err != nil {
		return err
	}

	bc.ParsedUsernameTemplate, err = template.New("username").Parse(bc.UsernameTemplate)
	if err != nil {
		return err
	} else if !strings.Contains(bc.FormatUsername("1234567890"), "1234567890") {
		return fmt.Errorf("username template is missing user ID placeholder")
	}

	bc.displaynameTemplate, err = template.New("displayname").Parse(bc.DisplaynameTemplate)
	if err != nil {
		return err
	}

	if bc.MessageHandlingTimeout.ErrorAfterStr != "" {
		bc.MessageHandlingTimeout.ErrorAfter, err = time.ParseDuration(bc.MessageHandlingTimeout.ErrorAfterStr)
		if err != nil {
			return err
		}
	}
	if bc.MessageHandlingTimeout.DeadlineStr != "" {
		bc.MessageHandlingTimeout.Deadline, err = time.ParseDuration(bc.MessageHandlingTimeout.DeadlineStr)
		if err != nil {
			return err
		}
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

const (
	NameQualityPush    = 3
	NameQualityContact = 2
	NameQualityPhone   = 1
)

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
		quality = NameQualityPush
	case len(contact.FullName) > 0 || len(contact.FirstName) > 0:
		quality = NameQualityContact
	default:
		quality = NameQualityPhone
	}
	return buf.String(), quality
}

func (bc BridgeConfig) FormatUsername(username string) string {
	var buf strings.Builder
	_ = bc.ParsedUsernameTemplate.Execute(&buf, username)
	return buf.String()
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
