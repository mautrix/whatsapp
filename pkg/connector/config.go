package connector

import (
	_ "embed"
	"strings"
	"text/template"

	up "go.mau.fi/util/configupgrade"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/event"
)

type MediaRequestMethod string

//go:embed example-config.yaml
var ExampleConfig string

type WhatsAppConfig struct {
	WhatsApp struct {
		OSName      string `yaml:"os_name"`
		BrowserName string `yaml:"browser_name"`

		Proxy          string `yaml:"proxy"`
		GetProxyURL    string `yaml:"get_proxy_url"`
		ProxyOnlyLogin bool   `yaml:"proxy_only_login"`
	} `yaml:"whatsapp"`

	Bridge struct {
		DisplaynameTemplate string `yaml:"displayname_template"`

		CallStartNotices bool `yaml:"call_start_notices"`

		HistorySync struct {
			RequestFullSync bool `yaml:"request_full_sync"`
			FullSyncConfig  struct {
				DaysLimit    uint32 `yaml:"days_limit"`
				SizeLimit    uint32 `yaml:"size_mb_limit"`
				StorageQuota uint32 `yaml:"storage_quota_mb"`
			} `yaml:"full_sync_config"`

			MediaRequests struct {
				AutoRequestMedia bool               `yaml:"auto_request_media"`
				RequestMethod    MediaRequestMethod `yaml:"request_method"`
				RequestLocalTime int                `yaml:"request_local_time"`
				MaxAsyncHandle   int64              `yaml:"max_async_handle"`
			} `yaml:"media_requests"`
		} `yaml:"history_sync"`

		UserAvatarSync bool `yaml:"user_avatar_sync"`

		SendPresenceOnTyping bool `yaml:"send_presence_on_typing"`

		ArchiveTag      event.RoomTag `yaml:"archive_tag"`
		PinnedTag       event.RoomTag `yaml:"pinned_tag"`
		TagOnlyOnCreate bool          `yaml:"tag_only_on_create"`

		EnableStatusBroadcast      bool          `yaml:"enable_status_broadcast"`
		DisableStatusBroadcastSend bool          `yaml:"disable_status_broadcast_send"`
		MuteStatusBroadcast        bool          `yaml:"mute_status_broadcast"`
		StatusBroadcastTag         event.RoomTag `yaml:"status_broadcast_tag"`

		WhatsappThumbnail bool `yaml:"whatsapp_thumbnail"`

		URLPreviews bool `yaml:"url_previews"`

		ForceActiveDeliveryReceipts bool `yaml:"force_active_delivery_receipts"`
	} `yaml:"bridge"`
	displaynameTemplate *template.Template `yaml:"-"`
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "whatsapp", "os_name")
	helper.Copy(up.Str, "whatsapp", "browser_name")

	helper.Copy(up.Str, "whatsapp", "proxy")
	helper.Copy(up.Str, "whatsapp", "get_proxy_url")
	helper.Copy(up.Bool, "whatsapp", "proxy_only_login")

	helper.Copy(up.Str, "bridge", "displayname_template")

	helper.Copy(up.Bool, "bridge", "call_start_notices")

	helper.Copy(up.Bool, "bridge", "history_sync", "request_full_sync")

	helper.Copy(up.Int, "bridge", "history_sync", "full_sync_config", "days_limit")
	helper.Copy(up.Int, "bridge", "history_sync", "full_sync_config", "size_mb_limit")
	helper.Copy(up.Int, "bridge", "history_sync", "full_sync_config", "storage_quota_mb")

	helper.Copy(up.Bool, "bridge", "history_sync", "media_requests", "auto_request_media")
	helper.Copy(up.Str, "bridge", "history_sync", "media_requests", "request_method")
	helper.Copy(up.Int, "bridge", "history_sync", "media_requests", "request_local_time")
	helper.Copy(up.Int, "bridge", "history_sync", "media_requests", "max_async_handle")

	helper.Copy(up.Bool, "bridge", "user_avatar_sync")

	helper.Copy(up.Bool, "bridge", "send_presence_on_typing")

	helper.Copy(up.Str, "bridge", "archive_tag")
	helper.Copy(up.Str, "bridge", "pinned_tag")
	helper.Copy(up.Bool, "bridge", "tag_only_on_create")

	helper.Copy(up.Bool, "bridge", "enable_status_broadcast")
	helper.Copy(up.Bool, "bridge", "disable_status_broadcast_send")
	helper.Copy(up.Bool, "bridge", "mute_status_broadcast")
	helper.Copy(up.Str, "bridge", "status_broadcast_tag")

	helper.Copy(up.Bool, "bridge", "whatsapp_thumbnail")

	helper.Copy(up.Bool, "bridge", "url_previews")
}

type DisplaynameParams struct {
	types.ContactInfo
	Phone string
	JID   string
}

func (c *WhatsAppConfig) FormatDisplayname(jid types.JID, contact types.ContactInfo) string {
	var nameBuf strings.Builder
	err := c.displaynameTemplate.Execute(&nameBuf, &DisplaynameParams{
		ContactInfo: contact,
		Phone:       "+" + jid.User,
		JID:         "+" + jid.User,
	})
	if err != nil {
		panic(err)
	}
	return nameBuf.String()
}

func (wa *WhatsAppConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, wa.Config, up.SimpleUpgrader(upgradeConfig)
}
