package voip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	lkpcm "github.com/livekit/media-sdk"
	livekitproto "github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
	"github.com/rs/zerolog"
)

type LiveKitParticipant struct {
	cfg      LiveKitConfig
	videoCfg VideoConfig
	log      zerolog.Logger
	room     *lksdk.Room
	audio    *lkmedia.PCMLocalTrack
	audioPub *lksdk.LocalTrackPublication
	audioSrc *MeowcallerAudioSource
	video    *lksdk.LocalTrack
	videoPub *lksdk.LocalTrackPublication

	mu                         sync.Mutex
	remoteAudio                []*lkmedia.PCMRemoteTrack
	remoteMediaCancel          context.CancelFunc
	disconnected               bool
	selectedRemoteParticipant  string
	remoteAudioMuteStateChange func(muted bool)
	remoteVideoFrame           func(frame LiveKitVideoFrame) error
	remoteVideoMuteStateChange func(muted bool)
}

func ConnectLiveKitParticipant(ctx context.Context, authResp *LiveKitAuthResponse, cfg LiveKitConfig, videoCfg VideoConfig, log zerolog.Logger) (*LiveKitParticipant, error) {
	if authResp == nil {
		return nil, fmt.Errorf("livekit auth response is nil")
	}
	if authResp.ConnectionURL() == "" || authResp.JWT() == "" {
		return nil, fmt.Errorf("livekit auth response did not include both URL and token")
	}
	remoteMediaCtx, remoteMediaCancel := context.WithCancel(context.Background())
	participant := &LiveKitParticipant{
		cfg:               cfg,
		videoCfg:          videoCfg,
		log:               log,
		audioSrc:          NewMeowcallerAudioSource(12),
		remoteMediaCancel: remoteMediaCancel,
	}
	callback := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				participant.onTrackSubscribed(remoteMediaCtx, track, publication, rp)
			},
			OnTrackUnsubscribed: participant.onTrackUnsubscribed,
			OnTrackMuted:        participant.onTrackMuted,
			OnTrackUnmuted:      participant.onTrackUnmuted,
		},
		OnDisconnected: func() {
			participant.closeRemoteTracks()
		},
		OnDisconnectedWithReason: func(reason lksdk.DisconnectionReason) {
			log.Info().Str("reason", string(reason)).Msg("Disconnected from LiveKit")
			participant.closeRemoteTracks()
		},
	}
	opts := []lksdk.ConnectOption{
		lksdk.WithAutoSubscribe(cfg.AutoSubscribe),
	}
	if cfg.ConnectTimeout > 0 {
		opts = append(opts, lksdk.WithConnectTimeout(cfg.ConnectTimeout))
	}
	room, err := connectLiveKit(ctx, authResp.ConnectionURL(), authResp.JWT(), callback, opts...)
	if err != nil {
		remoteMediaCancel()
		return nil, err
	}
	participant.room = room
	return participant, nil
}

func connectLiveKit(ctx context.Context, url, token string, callback *lksdk.RoomCallback, opts ...lksdk.ConnectOption) (*lksdk.Room, error) {
	room := lksdk.NewRoom(callback)
	if err := room.JoinWithContextAndToken(ctx, url, token, opts...); err != nil {
		return nil, err
	}
	return room, nil
}

func (p *LiveKitParticipant) SetRemoteAudioMuteHandler(selectedParticipant string, handler func(muted bool)) {
	p.mu.Lock()
	p.selectedRemoteParticipant = selectedParticipant
	p.remoteAudioMuteStateChange = handler
	p.mu.Unlock()
}

func (p *LiveKitParticipant) SetRemoteVideoHandlers(selectedParticipant string, frameHandler func(frame LiveKitVideoFrame) error, muteHandler func(muted bool)) {
	p.mu.Lock()
	p.selectedRemoteParticipant = selectedParticipant
	p.remoteVideoFrame = frameHandler
	p.remoteVideoMuteStateChange = muteHandler
	p.mu.Unlock()
}

func (p *LiveKitParticipant) PublishAudioTrack(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.room == nil {
		return fmt.Errorf("livekit room is not connected")
	}
	if p.audio != nil {
		return nil
	}
	track, err := lkmedia.NewPCMLocalTrack(meowcallerSampleRate, 1, logger.GetLogger())
	if err != nil {
		return err
	}
	if name == "" {
		name = "whatsapp-audio"
	}
	pub, err := p.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: name})
	if err != nil {
		track.Close()
		return err
	}
	p.audio = track
	p.audioPub = pub
	return nil
}

func (p *LiveKitParticipant) PublishVideoTrack(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.room == nil {
		return fmt.Errorf("livekit room is not connected")
	}
	if p.video != nil {
		return nil
	}
	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: liveKitH264ClockRate})
	if err != nil {
		return err
	}
	if name == "" {
		name = "whatsapp-video"
	}
	pub, err := p.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:        name,
		Source:      livekitproto.TrackSource_CAMERA,
		VideoWidth:  p.videoCfg.MaxWidth,
		VideoHeight: p.videoCfg.MaxHeight,
	})
	if err != nil {
		_ = track.Close()
		return err
	}
	p.video = track
	p.videoPub = pub
	return nil
}

func (p *LiveKitParticipant) SetWhatsAppAudioMuted(muted bool) {
	p.mu.Lock()
	pub := p.audioPub
	p.mu.Unlock()
	if pub == nil {
		return
	}
	pub.SetMuted(muted)
	p.log.Debug().Bool("muted", muted).Msg("Set LiveKit WhatsApp audio mute state")
}

func (p *LiveKitParticipant) SetWhatsAppVideoMuted(muted bool) {
	p.mu.Lock()
	pub := p.videoPub
	p.mu.Unlock()
	if pub == nil {
		return
	}
	pub.SetMuted(muted)
	p.log.Debug().Bool("muted", muted).Msg("Set LiveKit WhatsApp video mute state")
}

func (p *LiveKitParticipant) WhatsAppSink() *LiveKitPCMWriter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return NewLiveKitPCMWriter(p.audio)
}

func (p *LiveKitParticipant) WhatsAppVideoSink() *LiveKitH264Writer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return NewLiveKitH264Writer(p.video, videoFrameDuration(p.videoCfg))
}

func (p *LiveKitParticipant) MatrixAudioSource() *MeowcallerAudioSource {
	return p.audioSrc
}

func (p *LiveKitParticipant) WriteWhatsAppFrame(frame []float32) error {
	p.mu.Lock()
	audio := p.audio
	p.mu.Unlock()
	if audio == nil {
		return nil
	}
	return audio.WriteSample(Float32FrameToPCM16(frame))
}

func (p *LiveKitParticipant) Close() {
	p.mu.Lock()
	if p.disconnected {
		p.mu.Unlock()
		return
	}
	p.disconnected = true
	room := p.room
	audio := p.audio
	audioPub := p.audioPub
	video := p.video
	videoPub := p.videoPub
	p.room = nil
	p.audio = nil
	p.audioPub = nil
	p.video = nil
	p.videoPub = nil
	p.mu.Unlock()
	p.closeRemoteTracks()
	if audioPub != nil {
		audioPub.SetMuted(true)
	}
	if videoPub != nil {
		videoPub.SetMuted(true)
	}
	if audio != nil {
		audio.ClearQueue()
		_ = audio.Close()
	}
	if video != nil {
		_ = video.Close()
	}
	if room != nil {
		room.Disconnect()
	}
	_ = p.audioSrc.Close()
}

func (p *LiveKitParticipant) onTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	switch track.Kind() {
	case webrtc.RTPCodecTypeAudio:
		p.onAudioTrackSubscribed(track, publication, rp)
	case webrtc.RTPCodecTypeVideo:
		p.onVideoTrackSubscribed(ctx, track, publication, rp)
	}
}

func (p *LiveKitParticipant) onAudioTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if track.Codec().MimeType != webrtc.MimeTypeOpus {
		p.log.Warn().
			Str("codec", track.Codec().MimeType).
			Str("participant", string(rp.Identity())).
			Msg("Ignoring non-Opus LiveKit audio track")
		return
	}
	remote, err := lkmedia.NewPCMRemoteTrack(
		track,
		p.audioSrc,
		lkmedia.WithTargetSampleRate(meowcallerSampleRate),
		lkmedia.WithTargetChannels(1),
		lkmedia.WithLogger(logger.GetLogger()),
	)
	if err != nil {
		p.log.Warn().
			Err(err).
			Str("participant", string(rp.Identity())).
			Msg("Failed to subscribe LiveKit audio track")
		return
	}
	p.mu.Lock()
	p.remoteAudio = append(p.remoteAudio, remote)
	p.mu.Unlock()
	p.handleRemoteAudioMuteState(publication, rp, publication.IsMuted())
	_ = publication
}

func (p *LiveKitParticipant) onVideoTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if !p.videoCfg.Enabled {
		return
	}
	if !remoteParticipantSelected(p.selectedParticipant(), string(rp.Identity())) {
		p.log.Debug().
			Str("participant", string(rp.Identity())).
			Str("selected_participant", p.selectedParticipant()).
			Msg("Ignoring LiveKit video track from non-selected participant")
		return
	}
	if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeH264) {
		p.log.Warn().
			Str("codec", track.Codec().MimeType).
			Str("participant", string(rp.Identity())).
			Msg("Ignoring unsupported LiveKit video track; only H.264 passthrough is implemented")
		p.handleRemoteVideoMuteState(publication, rp, true)
		return
	}
	p.handleRemoteVideoMuteState(publication, rp, publication.IsMuted())
	go p.forwardRemoteH264Track(ctx, track, rp)
}

func (p *LiveKitParticipant) onTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		p.handleRemoteVideoMuteState(publication, rp, true)
	}
}

func (p *LiveKitParticipant) onTrackMuted(pub lksdk.TrackPublication, participant lksdk.Participant) {
	p.handleRemoteAudioMuteState(pub, participant, true)
	p.handleRemoteVideoMuteState(pub, participant, true)
}

func (p *LiveKitParticipant) onTrackUnmuted(pub lksdk.TrackPublication, participant lksdk.Participant) {
	p.handleRemoteAudioMuteState(pub, participant, false)
	p.handleRemoteVideoMuteState(pub, participant, false)
}

func (p *LiveKitParticipant) handleRemoteAudioMuteState(pub lksdk.TrackPublication, participant lksdk.Participant, muted bool) {
	if pub == nil || participant == nil || pub.Kind() != lksdk.TrackKindAudio {
		return
	}
	if _, ok := participant.(*lksdk.RemoteParticipant); !ok {
		return
	}
	identity := participant.Identity()
	p.mu.Lock()
	selected := p.selectedRemoteParticipant
	handler := p.remoteAudioMuteStateChange
	p.mu.Unlock()
	if selected != "" && identity != selected {
		p.log.Debug().
			Str("participant", identity).
			Str("selected_participant", selected).
			Bool("muted", muted).
			Msg("Ignoring LiveKit mute state from non-selected participant")
		return
	}
	p.log.Debug().
		Str("participant", identity).
		Str("track_id", pub.SID()).
		Bool("muted", muted).
		Msg("Observed LiveKit remote audio mute state")
	if handler != nil {
		handler(muted)
	}
}

func (p *LiveKitParticipant) handleRemoteVideoMuteState(pub lksdk.TrackPublication, participant lksdk.Participant, muted bool) {
	if pub == nil || participant == nil || pub.Kind() != lksdk.TrackKindVideo {
		return
	}
	if _, ok := participant.(*lksdk.RemoteParticipant); !ok {
		return
	}
	identity := participant.Identity()
	p.mu.Lock()
	selected := p.selectedRemoteParticipant
	handler := p.remoteVideoMuteStateChange
	p.mu.Unlock()
	if !remoteParticipantSelected(selected, identity) {
		p.log.Debug().
			Str("participant", identity).
			Str("selected_participant", selected).
			Bool("muted", muted).
			Msg("Ignoring LiveKit video mute state from non-selected participant")
		return
	}
	p.log.Debug().
		Str("participant", identity).
		Str("track_id", pub.SID()).
		Bool("muted", muted).
		Msg("Observed LiveKit remote video mute state")
	if handler != nil {
		handler(muted)
	}
}

func (p *LiveKitParticipant) forwardRemoteH264Track(ctx context.Context, track *webrtc.TrackRemote, rp *lksdk.RemoteParticipant) {
	builder := samplebuilder.New(
		liveKitH264MaxLatePackets,
		&codecs.H264Packet{},
		track.Codec().ClockRate,
	)
	p.log.Info().
		Str("participant", string(rp.Identity())).
		Str("track_id", track.ID()).
		Msg("Started forwarding LiveKit H.264 video to WhatsApp")
	for {
		if ctx.Err() != nil {
			return
		}
		packet, _, err := track.ReadRTP()
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				p.log.Debug().
					Err(err).
					Str("participant", string(rp.Identity())).
					Str("track_id", track.ID()).
					Msg("Stopped reading LiveKit H.264 video track")
			}
			return
		}
		builder.Push(packet)
		for sample := builder.Pop(); sample != nil; sample = builder.Pop() {
			if len(sample.Data) == 0 {
				continue
			}
			p.mu.Lock()
			handler := p.remoteVideoFrame
			p.mu.Unlock()
			if handler == nil {
				continue
			}
			if err = handler(LiveKitVideoFrame{
				AccessUnit: sample.Data,
				Duration:   sample.Duration,
			}); err != nil {
				p.log.Warn().
					Err(err).
					Str("participant", string(rp.Identity())).
					Str("track_id", track.ID()).
					Msg("Failed to forward LiveKit H.264 frame to WhatsApp")
			}
		}
	}
}

func (p *LiveKitParticipant) selectedParticipant() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.selectedRemoteParticipant
}

func remoteParticipantSelected(selected, identity string) bool {
	return selected == "" || identity == selected
}

func (p *LiveKitParticipant) closeRemoteTracks() {
	p.mu.Lock()
	tracks := p.remoteAudio
	p.remoteAudio = nil
	cancel := p.remoteMediaCancel
	p.remoteMediaCancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, track := range tracks {
		track.Close()
	}
}

const meowcallerSampleRate = 16000
const liveKitH264ClockRate = 90000
const liveKitH264MaxLatePackets = 1000

func videoFrameDuration(cfg VideoConfig) time.Duration {
	if cfg.MaxFPS <= 0 {
		return time.Second / 30
	}
	return time.Second / time.Duration(cfg.MaxFPS)
}

var _ lkpcm.PCM16Writer = (*MeowcallerAudioSource)(nil)
