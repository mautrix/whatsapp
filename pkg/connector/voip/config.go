package voip

import "time"

type Config struct {
	Enabled                bool
	IncomingPolicy         string
	MaxActiveCallsPerLogin int
	MatrixRTC              MatrixRTCConfig
	LiveKit                LiveKitConfig
	Audio                  AudioConfig
	Video                  VideoConfig
	Diagnostics            DiagnosticsConfig
}

type MatrixRTCConfig struct {
	LiveKitServiceURL       string
	RequireLiveKitFocus     bool
	MembershipEventCompat   string
	NotificationEventCompat string
	UseDelayedEvents        bool
	ParticipantMode         string
	FallbackParticipantMXID string
}

type LiveKitConfig struct {
	ConnectTimeout                     time.Duration
	PublishSilenceBeforeWhatsAppAnswer bool
	AutoSubscribe                      bool
	AudioUplinkPolicy                  string
	SelectedParticipantTimeout         time.Duration
}

type AudioConfig struct {
	Enabled            bool
	JitterBuffer       time.Duration
	OpusBackend        string
	SilenceOnUnderrun  bool
	MaxMixParticipants int
}

type VideoConfig struct {
	Enabled              bool
	SelectedSourcePolicy string
	MaxWidth             int
	MaxHeight            int
	MaxFPS               int
}

type DiagnosticsConfig struct {
	HealthcheckFocusOnStartup   bool
	EnableMeowcallerDiagnostics bool
	MediaTraceDir               string
}
