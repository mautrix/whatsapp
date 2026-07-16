package connector

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"

	up "go.mau.fi/util/configupgrade"
	"go.mau.fi/whatsmeow/types"
	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/msgconv"
)

type MediaRequestMethod string

const (
	MediaRequestMethodImmediate MediaRequestMethod = "immediate"
	MediaRequestMethodLocalTime MediaRequestMethod = "local_time"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	OSName      string `yaml:"os_name"`
	BrowserName string `yaml:"browser_name"`

	Proxy          string `yaml:"proxy"`
	GetProxyURL    string `yaml:"get_proxy_url"`
	ProxyOnlyLogin bool   `yaml:"proxy_only_login"`

	DisplaynameTemplate string `yaml:"displayname_template"`

	CallStartNotices            bool          `yaml:"call_start_notices"`
	IdentityChangeNotices       bool          `yaml:"identity_change_notices"`
	SendPresenceOnTyping        bool          `yaml:"send_presence_on_typing"`
	EnableStatusBroadcast       bool          `yaml:"enable_status_broadcast"`
	DisableStatusBroadcastSend  bool          `yaml:"disable_status_broadcast_send"`
	MuteStatusBroadcast         bool          `yaml:"mute_status_broadcast"`
	StatusBroadcastTag          event.RoomTag `yaml:"status_broadcast_tag"`
	PinnedTag                   event.RoomTag `yaml:"pinned_tag"`
	ArchiveTag                  event.RoomTag `yaml:"archive_tag"`
	WhatsappThumbnail           bool          `yaml:"whatsapp_thumbnail"`
	URLPreviews                 bool          `yaml:"url_previews"`
	ExtEvPolls                  bool          `yaml:"extev_polls"`
	DisableViewOnce             bool          `yaml:"disable_view_once"`
	ForceActiveDeliveryReceipts bool          `yaml:"force_active_delivery_receipts"`
	DirectMediaAutoRequest      bool          `yaml:"direct_media_auto_request"`
	InitialAutoReconnect        bool          `yaml:"initial_auto_reconnect"`
	UseWhatsAppRetryStore       bool          `yaml:"use_whatsapp_retry_store"`

	AnimatedSticker msgconv.AnimatedStickerConfig `yaml:"animated_sticker"`
	VOIP            VOIPConfig                    `yaml:"voip"`

	HistorySync struct {
		MaxInitialConversations int           `yaml:"max_initial_conversations"`
		RequestFullSync         bool          `yaml:"request_full_sync"`
		DispatchWait            time.Duration `yaml:"dispatch_wait"`
		FullSyncConfig          struct {
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

		BackwardsOnDemand bool `yaml:"backwards_on_demand"`
	} `yaml:"history_sync"`

	displaynameTemplate *template.Template `yaml:"-"`
}

type VOIPConfig struct {
	Enabled                bool            `yaml:"enabled"`
	MatrixSurface          string          `yaml:"matrix_surface"`
	IncomingPolicy         string          `yaml:"incoming_policy"`
	MaxActiveCallsPerLogin int             `yaml:"max_active_calls_per_login"`
	MatrixRTC              MatrixRTCConfig `yaml:"matrixrtc"`
	LiveKit                LiveKitConfig   `yaml:"livekit"`
	Audio                  VOIPAudioConfig `yaml:"audio"`
	Video                  VOIPVideoConfig `yaml:"video"`
	Diagnostics            VOIPDiagnostics `yaml:"diagnostics"`
}

type MatrixRTCConfig struct {
	LiveKitServiceURL       string `yaml:"livekit_service_url"`
	RequireLiveKitFocus     bool   `yaml:"require_livekit_focus"`
	MembershipEventCompat   string `yaml:"membership_event_compat"`
	NotificationEventCompat string `yaml:"notification_event_compat"`
	UseDelayedEvents        bool   `yaml:"use_delayed_events"`
	ParticipantMode         string `yaml:"participant_mode"`
	FallbackParticipantMXID string `yaml:"fallback_participant_mxid"`
}

type LiveKitConfig struct {
	ConnectTimeout                     time.Duration `yaml:"connect_timeout"`
	PublishSilenceBeforeWhatsAppAnswer bool          `yaml:"publish_silence_before_whatsapp_answer"`
	AutoSubscribe                      bool          `yaml:"auto_subscribe"`
	AudioUplinkPolicy                  string        `yaml:"audio_uplink_policy"`
	SelectedParticipantTimeout         time.Duration `yaml:"selected_participant_timeout"`
}

type VOIPAudioConfig struct {
	Enabled            bool          `yaml:"enabled"`
	JitterBuffer       time.Duration `yaml:"jitter_buffer_ms"`
	OpusBackend        string        `yaml:"opus_backend"`
	SilenceOnUnderrun  bool          `yaml:"silence_on_underrun"`
	MaxMixParticipants int           `yaml:"max_mix_participants"`
}

type VOIPVideoConfig struct {
	Enabled              bool   `yaml:"enabled"`
	SelectedSourcePolicy string `yaml:"selected_source_policy"`
	MaxWidth             int    `yaml:"max_width"`
	MaxHeight            int    `yaml:"max_height"`
	MaxFPS               int    `yaml:"max_fps"`
}

type VOIPDiagnostics struct {
	HealthcheckFocusOnStartup   bool   `yaml:"healthcheck_focus_on_startup"`
	EnableMeowcallerDiagnostics bool   `yaml:"enable_meowcaller_diagnostics"`
	MediaTraceDir               string `yaml:"media_trace_dir"`
}

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	err := node.Decode((*umConfig)(c))
	if err != nil {
		return err
	}
	return c.PostProcess()
}

func (c *Config) PostProcess() error {
	var err error
	c.displaynameTemplate, err = template.New("displayname").Parse(c.DisplaynameTemplate)
	if err != nil {
		return err
	}
	// Try to execute template to make sure it's valid
	_, err = c.formatDisplayname(types.PSAJID, "", types.ContactInfo{})
	if err != nil {
		return fmt.Errorf("failed to execute displayname template: %w", err)
	}
	if err = c.validateVOIP(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateVOIP() error {
	if !c.VOIP.Enabled {
		return nil
	}
	if c.VOIP.MatrixSurface != "matrixrtc_livekit" {
		return fmt.Errorf("voip.matrix_surface must be matrixrtc_livekit")
	}
	if !oneOf(c.VOIP.IncomingPolicy, "notice", "ring", "auto_answer") {
		return fmt.Errorf("voip.incoming_policy must be one of notice, ring, auto_answer")
	}
	if c.VOIP.MaxActiveCallsPerLogin <= 0 {
		return fmt.Errorf("voip.max_active_calls_per_login must be greater than 0")
	}
	if !oneOf(c.VOIP.MatrixRTC.MembershipEventCompat, "auto", "msc4143", "msc3401") {
		return fmt.Errorf("voip.matrixrtc.membership_event_compat must be one of auto, msc4143, msc3401")
	}
	if !oneOf(c.VOIP.MatrixRTC.NotificationEventCompat, "auto", "disabled") {
		return fmt.Errorf("voip.matrixrtc.notification_event_compat must be one of auto, disabled")
	}
	if !oneOf(c.VOIP.MatrixRTC.ParticipantMode, "whatsapp_ghost", "bridge_user") {
		return fmt.Errorf("voip.matrixrtc.participant_mode must be one of whatsapp_ghost, bridge_user")
	}
	if c.VOIP.LiveKit.ConnectTimeout <= 0 {
		return fmt.Errorf("voip.livekit.connect_timeout must be greater than 0")
	}
	if !oneOf(c.VOIP.LiveKit.AudioUplinkPolicy, "dominant_speaker", "mix_all", "selected_participant") {
		return fmt.Errorf("voip.livekit.audio_uplink_policy must be one of dominant_speaker, mix_all, selected_participant")
	}
	if c.VOIP.Audio.Enabled {
		if c.VOIP.Audio.JitterBuffer <= 0 {
			return fmt.Errorf("voip.audio.jitter_buffer_ms must be greater than 0")
		}
		if c.VOIP.Audio.OpusBackend == "" {
			return fmt.Errorf("voip.audio.opus_backend must be set")
		}
		if c.VOIP.Audio.MaxMixParticipants <= 0 {
			return fmt.Errorf("voip.audio.max_mix_participants must be greater than 0")
		}
	}
	if c.VOIP.Video.Enabled {
		if !oneOf(c.VOIP.Video.SelectedSourcePolicy, "active_speaker", "selected_participant") {
			return fmt.Errorf("voip.video.selected_source_policy must be one of active_speaker, selected_participant")
		}
		if c.VOIP.Video.MaxWidth <= 0 || c.VOIP.Video.MaxHeight <= 0 || c.VOIP.Video.MaxFPS <= 0 {
			return fmt.Errorf("voip.video max_width, max_height and max_fps must be greater than 0")
		}
	}
	if c.VOIP.Diagnostics.EnableMeowcallerDiagnostics && c.VOIP.Diagnostics.MediaTraceDir == "" {
		return fmt.Errorf("voip.diagnostics.media_trace_dir must be set when meowcaller diagnostics are enabled")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "os_name")
	helper.Copy(up.Str, "browser_name")

	helper.Copy(up.Str|up.Null, "proxy")
	helper.Copy(up.Str|up.Null, "get_proxy_url")
	helper.Copy(up.Bool, "proxy_only_login")

	helper.Copy(up.Str, "displayname_template")

	helper.Copy(up.Bool, "call_start_notices")
	helper.Copy(up.Bool, "identity_change_notices")
	helper.Copy(up.Bool, "send_presence_on_typing")
	helper.Copy(up.Bool, "enable_status_broadcast")
	helper.Copy(up.Bool, "disable_status_broadcast_send")
	helper.Copy(up.Bool, "mute_status_broadcast")
	helper.Copy(up.Str|up.Null, "status_broadcast_tag")
	helper.Copy(up.Str|up.Null, "pinned_tag")
	helper.Copy(up.Str|up.Null, "archive_tag")
	helper.Copy(up.Bool, "whatsapp_thumbnail")
	helper.Copy(up.Bool, "url_previews")
	helper.Copy(up.Bool, "extev_polls")
	helper.Copy(up.Bool, "disable_view_once")
	helper.Copy(up.Bool, "force_active_delivery_receipts")
	helper.Copy(up.Bool, "direct_media_auto_request")
	helper.Copy(up.Bool, "initial_auto_reconnect")
	helper.Copy(up.Bool, "use_whatsapp_retry_store")

	helper.Copy(up.Str, "animated_sticker", "target")
	helper.Copy(up.Int, "animated_sticker", "args", "width")
	helper.Copy(up.Int, "animated_sticker", "args", "height")
	helper.Copy(up.Int, "animated_sticker", "args", "fps")

	helper.Copy(up.Bool, "voip", "enabled")
	helper.Copy(up.Str, "voip", "matrix_surface")
	helper.Copy(up.Str, "voip", "incoming_policy")
	helper.Copy(up.Int, "voip", "max_active_calls_per_login")
	helper.Copy(up.Str|up.Null, "voip", "matrixrtc", "livekit_service_url")
	helper.Copy(up.Bool, "voip", "matrixrtc", "require_livekit_focus")
	helper.Copy(up.Str, "voip", "matrixrtc", "membership_event_compat")
	helper.Copy(up.Str, "voip", "matrixrtc", "notification_event_compat")
	helper.Copy(up.Bool, "voip", "matrixrtc", "use_delayed_events")
	helper.Copy(up.Str, "voip", "matrixrtc", "participant_mode")
	helper.Copy(up.Str|up.Null, "voip", "matrixrtc", "fallback_participant_mxid")
	helper.Copy(up.Str|up.Int, "voip", "livekit", "connect_timeout")
	helper.Copy(up.Bool, "voip", "livekit", "publish_silence_before_whatsapp_answer")
	helper.Copy(up.Bool, "voip", "livekit", "auto_subscribe")
	helper.Copy(up.Str, "voip", "livekit", "audio_uplink_policy")
	helper.Copy(up.Str|up.Int, "voip", "livekit", "selected_participant_timeout")
	helper.Copy(up.Bool, "voip", "audio", "enabled")
	helper.Copy(up.Str|up.Int, "voip", "audio", "jitter_buffer_ms")
	helper.Copy(up.Str, "voip", "audio", "opus_backend")
	helper.Copy(up.Bool, "voip", "audio", "silence_on_underrun")
	helper.Copy(up.Int, "voip", "audio", "max_mix_participants")
	helper.Copy(up.Bool, "voip", "video", "enabled")
	helper.Copy(up.Str, "voip", "video", "selected_source_policy")
	helper.Copy(up.Int, "voip", "video", "max_width")
	helper.Copy(up.Int, "voip", "video", "max_height")
	helper.Copy(up.Int, "voip", "video", "max_fps")
	helper.Copy(up.Bool, "voip", "diagnostics", "healthcheck_focus_on_startup")
	helper.Copy(up.Bool, "voip", "diagnostics", "enable_meowcaller_diagnostics")
	helper.Copy(up.Str|up.Null, "voip", "diagnostics", "media_trace_dir")

	helper.Copy(up.Int, "history_sync", "max_initial_conversations")
	helper.Copy(up.Bool, "history_sync", "request_full_sync")
	helper.Copy(up.Str|up.Int, "history_sync", "dispatch_wait")
	helper.Copy(up.Int|up.Null, "history_sync", "full_sync_config", "days_limit")
	helper.Copy(up.Int|up.Null, "history_sync", "full_sync_config", "size_mb_limit")
	helper.Copy(up.Int|up.Null, "history_sync", "full_sync_config", "storage_quota_mb")
	helper.Copy(up.Bool, "history_sync", "media_requests", "auto_request_media")
	helper.Copy(up.Str, "history_sync", "media_requests", "request_method")
	helper.Copy(up.Int, "history_sync", "media_requests", "request_local_time")
	helper.Copy(up.Int, "history_sync", "media_requests", "max_async_handle")
	helper.Copy(up.Bool, "history_sync", "backwards_on_demand")
}

type DisplaynameParams struct {
	types.ContactInfo
	Phone string

	// Deprecated legacy fields
	JID    string
	Notify string
	VName  string
	Name   string
	Short  string
}

func (c *Config) formatDisplayname(jid types.JID, phone string, contact types.ContactInfo) (string, error) {
	var nameBuf strings.Builder
	if phone == "" && jid.Server == types.DefaultUserServer {
		phone = "+" + jid.User
	}
	if contact.RedactedPhone == "" && phone != "" {
		contact.RedactedPhone = redactPhone(phone)
	}
	err := c.displaynameTemplate.Execute(&nameBuf, &DisplaynameParams{
		ContactInfo: contact,
		Phone:       phone,

		// Deprecated legacy fields
		JID:    phone,
		Notify: contact.PushName,
		VName:  contact.BusinessName,
		Name:   contact.FullName,
		Short:  contact.FirstName,
	})
	return nameBuf.String(), err
}

func (c *Config) FormatDisplayname(jid types.JID, phone string, contact types.ContactInfo) string {
	name, err := c.formatDisplayname(jid, phone, contact)
	if err != nil {
		panic(err)
	}
	return name
}

func redactPhone(phone string) string {
	if len(phone) <= 4 {
		return phone
	}
	// This doesn't keep 2+ digit country codes properly, but whatever
	return phone[:2] + strings.Repeat("∙", len(phone)-4) + phone[len(phone)-2:]
}

func (wa *WhatsAppConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &wa.Config, &up.StructUpgrader{
		SimpleUpgrader: up.SimpleUpgrader(upgradeConfig),
		Blocks: [][]string{
			{"proxy"},
			{"displayname_template"},
			{"call_start_notices"},
			{"voip"},
			{"history_sync"},
		},
		Base: ExampleConfig,
	}
}
