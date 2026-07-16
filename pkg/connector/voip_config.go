package connector

import "go.mau.fi/mautrix-whatsapp/pkg/connector/voip"

func makeVOIPConfig(cfg VOIPConfig) voip.Config {
	return voip.Config{
		Enabled:                cfg.Enabled,
		IncomingPolicy:         cfg.IncomingPolicy,
		MaxActiveCallsPerLogin: cfg.MaxActiveCallsPerLogin,
		MatrixRTC: voip.MatrixRTCConfig{
			LiveKitServiceURL:       cfg.MatrixRTC.LiveKitServiceURL,
			RequireLiveKitFocus:     cfg.MatrixRTC.RequireLiveKitFocus,
			MembershipEventCompat:   cfg.MatrixRTC.MembershipEventCompat,
			NotificationEventCompat: cfg.MatrixRTC.NotificationEventCompat,
			UseDelayedEvents:        cfg.MatrixRTC.UseDelayedEvents,
			ParticipantMode:         cfg.MatrixRTC.ParticipantMode,
			FallbackParticipantMXID: cfg.MatrixRTC.FallbackParticipantMXID,
		},
		LiveKit: voip.LiveKitConfig{
			ConnectTimeout:                     cfg.LiveKit.ConnectTimeout,
			PublishSilenceBeforeWhatsAppAnswer: cfg.LiveKit.PublishSilenceBeforeWhatsAppAnswer,
			AutoSubscribe:                      cfg.LiveKit.AutoSubscribe,
			AudioUplinkPolicy:                  cfg.LiveKit.AudioUplinkPolicy,
			SelectedParticipantTimeout:         cfg.LiveKit.SelectedParticipantTimeout,
		},
		Audio: voip.AudioConfig{
			Enabled:            cfg.Audio.Enabled,
			JitterBuffer:       cfg.Audio.JitterBuffer,
			OpusBackend:        cfg.Audio.OpusBackend,
			SilenceOnUnderrun:  cfg.Audio.SilenceOnUnderrun,
			MaxMixParticipants: cfg.Audio.MaxMixParticipants,
		},
		Video: voip.VideoConfig{
			Enabled:              cfg.Video.Enabled,
			SelectedSourcePolicy: cfg.Video.SelectedSourcePolicy,
			MaxWidth:             cfg.Video.MaxWidth,
			MaxHeight:            cfg.Video.MaxHeight,
			MaxFPS:               cfg.Video.MaxFPS,
		},
		Diagnostics: voip.DiagnosticsConfig{
			HealthcheckFocusOnStartup:   cfg.Diagnostics.HealthcheckFocusOnStartup,
			EnableMeowcallerDiagnostics: cfg.Diagnostics.EnableMeowcallerDiagnostics,
			MediaTraceDir:               cfg.Diagnostics.MediaTraceDir,
		},
	}
}
