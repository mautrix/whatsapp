package voip

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/diag"
	"github.com/purpshell/meowcaller/signaling"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

const (
	localUnmuteState = "0"
	localMuteState   = "1"
)

type matrixVideoAction uint8

const (
	matrixVideoNone matrixVideoAction = iota
	matrixVideoDisable
	matrixVideoEnable
	matrixVideoUpgrade
)

var localMuteRetryIntervals = []time.Duration{
	0,
	2 * time.Second,
	2 * time.Second,
	2 * time.Second,
}

type Manager struct {
	cfg      Config
	waClient *whatsmeow.Client
	client   *meowcaller.Client
	log      zerolog.Logger

	mu                   sync.Mutex
	calls                map[string]*meowcaller.Call
	callCreators         map[string]types.JID
	livekit              map[string]*LiveKitParticipant
	livekitConnecting    map[string]struct{}
	matrixAudioMuted     map[string]bool
	matrixVideoMuted     map[string]bool
	whatsAppMuted        map[string]bool
	whatsAppVideoMuted   map[string]bool
	videoKeyframePending map[string]bool
	incomingCallNotify   func(*meowcaller.Call)
	callEndNotify        func(callID, reason string)
}

func NewManager(waClient *whatsmeow.Client, cfg Config, log zerolog.Logger) *Manager {
	manager := &Manager{
		cfg:                  cfg,
		waClient:             waClient,
		log:                  log,
		calls:                make(map[string]*meowcaller.Call),
		callCreators:         make(map[string]types.JID),
		livekit:              make(map[string]*LiveKitParticipant),
		livekitConnecting:    make(map[string]struct{}),
		matrixAudioMuted:     make(map[string]bool),
		matrixVideoMuted:     make(map[string]bool),
		whatsAppMuted:        make(map[string]bool),
		whatsAppVideoMuted:   make(map[string]bool),
		videoKeyframePending: make(map[string]bool),
	}
	if !cfg.Enabled || waClient == nil {
		return manager
	}
	opts := []meowcaller.Option{meowcaller.WithLogger(log)}
	if cfg.Diagnostics.EnableMeowcallerDiagnostics {
		rec, err := diag.NewRecorder(cfg.Diagnostics.MediaTraceDir)
		if err != nil {
			log.Warn().
				Err(err).
				Str("media_trace_dir", cfg.Diagnostics.MediaTraceDir).
				Msg("Failed to enable meowcaller media diagnostics")
		} else {
			opts = append(opts, meowcaller.WithDiagnostics(rec))
			log.Warn().
				Str("media_trace_dir", cfg.Diagnostics.MediaTraceDir).
				Msg("Enabled unsafe meowcaller media diagnostics")
		}
	}
	manager.client = meowcaller.NewClient(waClient, opts...)
	manager.client.OnIncomingCall(manager.handleIncomingCall)
	return manager
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled && m.client != nil
}

func (m *Manager) Client() *meowcaller.Client {
	if m == nil {
		return nil
	}
	return m.client
}

func (m *Manager) SetIncomingCallHandler(handler func(*meowcaller.Call)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.incomingCallNotify = handler
	m.mu.Unlock()
}

func (m *Manager) SetCallEndHandler(handler func(callID, reason string)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.callEndNotify = handler
	m.mu.Unlock()
}

func (m *Manager) Dial(ctx context.Context, target string, video ...bool) (*meowcaller.Call, error) {
	if !m.Enabled() {
		return nil, ErrNotEnabled
	}
	opts := meowcaller.CallOptions{}
	if len(video) > 0 {
		opts.Video = video[0]
	}
	call, err := m.client.CallWithOptions(ctx, target, opts)
	if err != nil {
		return nil, err
	}
	m.trackCall(call, m.ownCallCreator())
	return call, nil
}

func (m *Manager) AbortAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	calls := make([]*meowcaller.Call, 0, len(m.calls))
	for _, call := range m.calls {
		calls = append(calls, call)
	}
	m.calls = make(map[string]*meowcaller.Call)
	m.callCreators = make(map[string]types.JID)
	m.matrixAudioMuted = make(map[string]bool)
	m.matrixVideoMuted = make(map[string]bool)
	m.whatsAppMuted = make(map[string]bool)
	m.whatsAppVideoMuted = make(map[string]bool)
	m.videoKeyframePending = make(map[string]bool)
	participants := make([]*LiveKitParticipant, 0, len(m.livekit))
	for _, participant := range m.livekit {
		participants = append(participants, participant)
	}
	m.livekit = make(map[string]*LiveKitParticipant)
	m.livekitConnecting = make(map[string]struct{})
	m.mu.Unlock()
	for _, participant := range participants {
		participant.Close()
	}
	for _, call := range calls {
		if err := call.Hangup(); err != nil {
			m.log.Debug().Err(err).Str("call_id", call.ID()).Msg("Failed to hang up VOIP call during abort")
		}
	}
}

func (m *Manager) BridgeCallToLiveKit(ctx context.Context, waCallID string, authResp *LiveKitAuthResponse, selectedRemoteParticipantID string) error {
	if !m.Enabled() {
		return ErrNotEnabled
	}
	m.mu.Lock()
	call := m.calls[waCallID]
	existing := m.livekit[waCallID]
	_, connecting := m.livekitConnecting[waCallID]
	if call != nil && existing == nil && !connecting {
		m.livekitConnecting[waCallID] = struct{}{}
	}
	m.mu.Unlock()
	if call == nil {
		return ErrCallNotFound
	}
	if existing != nil || connecting {
		return nil
	}
	participant, err := ConnectLiveKitParticipant(ctx, authResp, m.cfg.LiveKit, m.cfg.Video, m.log.With().Str("call_id", waCallID).Str("component", "livekit").Logger())
	if err != nil {
		m.clearLiveKitConnecting(waCallID)
		return err
	}
	participant.SetRemoteAudioMuteHandler(selectedRemoteParticipantID, func(muted bool) {
		m.handleMatrixAudioMuteState(call, muted)
	})
	videoEnabled := m.cfg.Video.Enabled
	if videoEnabled {
		var videoBuffer whatsAppVideoStartupBuffer
		var videoBufferLock sync.Mutex
		participant.SetRemoteVideoHandlers(selectedRemoteParticipantID, func(frame LiveKitVideoFrame) error {
			if call.State() == meowcaller.CallPhaseEnded {
				return nil
			}
			videoBufferLock.Lock()
			bufferedBefore := videoBuffer.Len()
			flushed, err := videoBuffer.Send(frame, func(frame LiveKitVideoFrame) error {
				return call.SendVideoWithDuration(frame.AccessUnit, frame.Duration)
			})
			bufferedAfter := videoBuffer.Len()
			videoBufferLock.Unlock()
			if err != nil {
				m.log.Debug().
					Err(err).
					Str("call_id", call.ID()).
					Int("buffered_frames", bufferedAfter).
					Msg("Buffered LiveKit H.264 frame until WhatsApp video media is ready")
				return nil
			}
			if bufferedBefore > 0 && bufferedAfter == 0 {
				m.log.Info().
					Str("call_id", call.ID()).
					Int("flushed_frames", flushed).
					Msg("Flushed buffered LiveKit H.264 video to WhatsApp")
			}
			return nil
		}, func(muted bool) {
			m.handleMatrixVideoMuteState(call, muted)
		})
	}
	if err = participant.PublishAudioTrack("whatsapp-audio"); err != nil {
		participant.Close()
		m.clearLiveKitConnecting(waCallID)
		return err
	}
	call.Receive(participant.WhatsAppSink())
	call.Play(participant.MatrixAudioSource())
	if videoEnabled {
		if err = participant.PublishVideoTrack("whatsapp-video"); err != nil {
			call.Receive(nil)
			call.Subscribe(nil)
			participant.Close()
			m.clearLiveKitConnecting(waCallID)
			return err
		}
		call.ReceiveVideo(participant.WhatsAppVideoSink())
	}
	answeredIncoming := call.State() == meowcaller.CallPhaseRinging
	if call.State() == meowcaller.CallPhaseRinging {
		if err = call.Answer(); err != nil {
			call.Receive(nil)
			call.ReceiveVideo(nil)
			call.Subscribe(nil)
			participant.Close()
			m.clearLiveKitConnecting(waCallID)
			return err
		}
	}
	m.mu.Lock()
	if m.calls[waCallID] != call || call.State() == meowcaller.CallPhaseEnded {
		delete(m.livekitConnecting, waCallID)
		delete(m.videoKeyframePending, waCallID)
		m.mu.Unlock()
		call.Receive(nil)
		call.ReceiveVideo(nil)
		call.Subscribe(nil)
		participant.Close()
		return ErrCallNotFound
	}
	whatsAppMuted, knownWhatsAppMute := m.whatsAppMuted[waCallID]
	whatsAppVideoMuted, knownWhatsAppVideoMute := m.whatsAppVideoMuted[waCallID]
	keyframePending := m.videoKeyframePending[waCallID]
	delete(m.videoKeyframePending, waCallID)
	delete(m.livekitConnecting, waCallID)
	m.livekit[waCallID] = participant
	m.mu.Unlock()
	if videoEnabled && keyframePending {
		participant.requestRemoteVideoKeyframe()
	}
	if knownWhatsAppMute {
		participant.SetWhatsAppAudioMuted(whatsAppMuted)
	}
	if videoEnabled {
		if !knownWhatsAppVideoMute {
			whatsAppVideoMuted = !call.IsReceivingVideo()
		}
		participant.SetWhatsAppVideoMuted(whatsAppVideoMuted)
	}
	if answeredIncoming {
		m.log.Debug().Str("call_id", waCallID).Msg("Answered incoming WhatsApp call before sending local unmute")
	}
	go m.sendLocalMuteStateRetries(call)
	return nil
}

func (m *Manager) sendLocalMuteStateRetries(call *meowcaller.Call) {
	if m == nil || m.waClient == nil || call == nil {
		return
	}
	for attempt, interval := range localMuteRetryIntervals {
		time.Sleep(interval)
		if call.State() == meowcaller.CallPhaseEnded {
			return
		}
		muted := m.currentMatrixAudioMuted(call.ID())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := m.sendLocalMuteState(ctx, call.ID(), call.Peer(), m.callCreatorFor(call), localMuteStateFor(muted))
		cancel()
		if err != nil {
			m.log.Warn().
				Err(err).
				Str("call_id", call.ID()).
				Stringer("peer_jid", call.Peer()).
				Bool("muted", muted).
				Int("attempt", attempt+1).
				Msg("Failed to send WhatsApp local mute state")
			continue
		}
		m.log.Debug().
			Str("call_id", call.ID()).
			Stringer("peer_jid", call.Peer()).
			Bool("muted", muted).
			Int("attempt", attempt+1).
			Msg("Sent WhatsApp local mute state")
	}
}

func (m *Manager) handleMatrixAudioMuteState(call *meowcaller.Call, muted bool) {
	if m == nil || call == nil || call.State() == meowcaller.CallPhaseEnded {
		return
	}
	m.mu.Lock()
	previous, known := m.matrixAudioMuted[call.ID()]
	m.matrixAudioMuted[call.ID()] = muted
	m.mu.Unlock()
	if known && previous == muted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := m.sendLocalMuteState(ctx, call.ID(), call.Peer(), m.callCreatorFor(call), localMuteStateFor(muted))
	cancel()
	if err != nil {
		m.log.Warn().
			Err(err).
			Str("call_id", call.ID()).
			Stringer("peer_jid", call.Peer()).
			Bool("muted", muted).
			Msg("Failed to send WhatsApp local mute state from LiveKit")
		return
	}
	m.log.Debug().
		Str("call_id", call.ID()).
		Stringer("peer_jid", call.Peer()).
		Bool("muted", muted).
		Msg("Sent WhatsApp local mute state from LiveKit")
}

func (m *Manager) handleMatrixVideoMuteState(call *meowcaller.Call, muted bool) {
	if m == nil || call == nil || call.State() == meowcaller.CallPhaseEnded {
		return
	}
	m.mu.Lock()
	previous, known := m.matrixVideoMuted[call.ID()]
	m.matrixVideoMuted[call.ID()] = muted
	m.mu.Unlock()
	if known && previous == muted {
		return
	}
	err := applyMatrixVideoState(call, muted)
	if err != nil {
		m.log.Warn().
			Err(err).
			Str("call_id", call.ID()).
			Stringer("peer_jid", call.Peer()).
			Bool("muted", muted).
			Msg("Failed to send WhatsApp local video state from LiveKit")
		return
	}
	m.log.Debug().
		Str("call_id", call.ID()).
		Stringer("peer_jid", call.Peer()).
		Bool("muted", muted).
		Msg("Sent WhatsApp local video state from LiveKit")
}

func (m *Manager) handleWhatsAppAudioMuteState(callID string, muted bool) {
	if m == nil || callID == "" {
		return
	}
	m.mu.Lock()
	m.whatsAppMuted[callID] = muted
	participant := m.livekit[callID]
	m.mu.Unlock()
	if participant != nil {
		participant.SetWhatsAppAudioMuted(muted)
	}
	m.log.Debug().
		Str("call_id", callID).
		Bool("muted", muted).
		Msg("Observed WhatsApp remote audio mute state")
}

func (m *Manager) handleWhatsAppVideoState(callID string, state meowcaller.VideoState) {
	if m == nil || callID == "" {
		return
	}
	muted, muteChanged := remoteVideoMuteForState(state)
	m.mu.Lock()
	if muteChanged {
		m.whatsAppVideoMuted[callID] = muted
	}
	participant := m.livekit[callID]
	m.mu.Unlock()
	if participant != nil {
		if muteChanged {
			participant.SetWhatsAppVideoMuted(muted)
		}
		participant.SetWhatsAppVideoOrientation(state.Orientation)
	}
	m.log.Debug().
		Str("call_id", callID).
		Bool("muted", muted).
		Bool("mute_changed", muteChanged).
		Bool("active", state.Active).
		Bool("upgrade", state.Upgrade).
		Int("orientation", state.Orientation).
		Int("raw_state", state.Raw).
		Msg("Observed WhatsApp remote video state")
}

func remoteVideoMuteForState(state meowcaller.VideoState) (muted, changed bool) {
	switch state.Raw {
	case signaling.VideoStateEnabled:
		return false, true
	case signaling.VideoStateDisabled, signaling.VideoStateStopped:
		return true, true
	default:
		return false, false
	}
}

func (m *Manager) currentMatrixAudioMuted(callID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	muted := m.matrixAudioMuted[callID]
	m.mu.Unlock()
	return muted
}

func localMuteStateFor(muted bool) string {
	if muted {
		return localMuteState
	}
	return localUnmuteState
}

func matrixVideoActionFor(muted, sending, receiving bool) matrixVideoAction {
	if muted {
		if sending {
			return matrixVideoDisable
		}
		return matrixVideoNone
	}
	if sending || receiving {
		return matrixVideoEnable
	}
	return matrixVideoUpgrade
}

func applyMatrixVideoState(call *meowcaller.Call, muted bool) error {
	switch matrixVideoActionFor(muted, call.IsSendingVideo(), call.IsReceivingVideo()) {
	case matrixVideoDisable:
		return call.SetVideoEnabled(false)
	case matrixVideoEnable:
		return call.SetVideoEnabled(true)
	case matrixVideoUpgrade:
		return call.StartVideo()
	default:
		return nil
	}
}

func (m *Manager) sendLocalMuteState(ctx context.Context, callID string, peer, callCreator types.JID, muteState string) error {
	if m == nil || m.waClient == nil {
		return fmt.Errorf("whatsapp client is not available")
	}
	if callID == "" {
		return fmt.Errorf("call ID is empty")
	}
	if peer.IsEmpty() {
		return fmt.Errorf("peer JID is empty")
	}
	if callCreator.IsEmpty() {
		return fmt.Errorf("call creator JID is empty")
	}
	node := buildLocalMuteV2Node(callID, peer, callCreator, string(m.waClient.GenerateMessageID()), muteState)
	//lint:ignore SA1019 low-level call signaling is not exposed by whatsmeow's public API
	if err := m.waClient.DangerousInternals().SendNode(ctx, node); err != nil {
		return fmt.Errorf("send mute_v2: %w", err)
	}
	return nil
}

func buildLocalMuteV2Node(callID string, peer, callCreator types.JID, wrapperID, muteState string) waBinary.Node {
	node := signaling.BuildMuteV2(callID, peer, callCreator, muteState)
	if wrapperID != "" {
		node.Attrs["id"] = wrapperID
	}
	return node
}

func (m *Manager) HandleMatrixRTCEvent(ctx context.Context, evt MatrixRTCEvent) int {
	return m.HandleMatrixRTCCallEvent(ctx, evt, "")
}

func (m *Manager) HandleMatrixRTCCallEvent(_ context.Context, evt MatrixRTCEvent, waCallID string) int {
	if !m.Enabled() {
		return 0
	}
	switch evt.Kind {
	case MatrixRTCEventKindRTCDecline:
		ended := m.endCallsFromMatrixRTC(waCallID)
		if ended == 0 {
			m.log.Debug().
				Stringer("matrix_room_id", evt.RoomID).
				Stringer("matrix_sender", evt.Sender).
				Str("matrix_call_id", evt.CallID).
				Str("wa_call_id", waCallID).
				Msg("Received MatrixRTC decline with no active WhatsApp VOIP calls")
		} else {
			m.log.Info().
				Stringer("matrix_room_id", evt.RoomID).
				Stringer("matrix_sender", evt.Sender).
				Str("matrix_call_id", evt.CallID).
				Str("wa_call_id", waCallID).
				Int("ended_call_count", ended).
				Msg("Ended WhatsApp VOIP calls after MatrixRTC decline")
		}
		return ended
	case MatrixRTCEventKindRTCMembership, MatrixRTCEventKindGroupCallMember, MatrixRTCEventKindRTCNotification, MatrixRTCEventKindLegacyCallNotify, MatrixRTCEventKindGroupCall:
		m.log.Debug().
			Stringer("matrix_room_id", evt.RoomID).
			Stringer("matrix_sender", evt.Sender).
			Str("matrix_call_id", evt.CallID).
			Str("wa_call_id", waCallID).
			Str("matrixrtc_kind", string(evt.Kind)).
			Msg("Observed MatrixRTC event")
	}
	return 0
}

func (m *Manager) handleIncomingCall(call *meowcaller.Call) {
	m.trackCall(call, call.Peer())
	m.log.Info().
		Str("call_id", call.ID()).
		Stringer("peer_jid", call.Peer()).
		Bool("video", call.IsVideo()).
		Msg("Received incoming WhatsApp call for MatrixRTC bridge")
	if m.cfg.IncomingPolicy == "notice" {
		if err := call.Reject(); err != nil {
			m.log.Warn().Err(err).Str("call_id", call.ID()).Msg("Failed to reject VOIP call handled as notice")
		}
		return
	}
	m.mu.Lock()
	handler := m.incomingCallNotify
	m.mu.Unlock()
	if handler != nil {
		go handler(call)
	}
}

func (m *Manager) trackCall(call *meowcaller.Call, callCreator types.JID) {
	if call == nil {
		return
	}
	if callCreator.IsEmpty() {
		callCreator = call.Peer()
	}
	m.mu.Lock()
	m.calls[call.ID()] = call
	m.callCreators[call.ID()] = callCreator
	m.mu.Unlock()
	call.OnEnd(func(reason string) {
		m.mu.Lock()
		delete(m.calls, call.ID())
		delete(m.callCreators, call.ID())
		delete(m.matrixAudioMuted, call.ID())
		delete(m.matrixVideoMuted, call.ID())
		delete(m.whatsAppMuted, call.ID())
		delete(m.whatsAppVideoMuted, call.ID())
		delete(m.videoKeyframePending, call.ID())
		participant := m.livekit[call.ID()]
		delete(m.livekit, call.ID())
		delete(m.livekitConnecting, call.ID())
		handler := m.callEndNotify
		m.mu.Unlock()
		if participant != nil {
			participant.Close()
		}
		m.log.Info().Str("call_id", call.ID()).Str("reason", reason).Msg("WhatsApp VOIP call ended")
		if handler != nil {
			go handler(call.ID(), reason)
		}
	})
	call.OnStateChange(func(phase meowcaller.CallPhase) {
		m.log.Debug().Str("call_id", call.ID()).Int("phase", int(phase)).Msg("WhatsApp VOIP call state changed")
	})
	call.OnPeerAccept(func() {
		if call.IsVideo() {
			m.requestLiveKitVideoKeyframe(call.ID())
		}
	})
	call.OnVideoKeyframeRequest(func() {
		m.requestLiveKitVideoKeyframe(call.ID())
	})
	call.OnMuteState(func(muted bool) {
		m.handleWhatsAppAudioMuteState(call.ID(), muted)
	})
	call.OnVideoState(func(state meowcaller.VideoState) {
		m.handleWhatsAppVideoState(call.ID(), state)
	})
}

func (m *Manager) requestLiveKitVideoKeyframe(callID string) {
	m.mu.Lock()
	call := m.calls[callID]
	participant := m.livekit[callID]
	if call != nil && participant == nil {
		m.videoKeyframePending[callID] = true
	}
	m.mu.Unlock()
	if call != nil && participant != nil {
		participant.requestRemoteVideoKeyframe()
	}
}

func (m *Manager) callCreatorFor(call *meowcaller.Call) types.JID {
	if m == nil || call == nil {
		return types.EmptyJID
	}
	m.mu.Lock()
	callCreator := m.callCreators[call.ID()]
	m.mu.Unlock()
	if !callCreator.IsEmpty() {
		return callCreator
	}
	if call.State() == meowcaller.CallPhaseCalling {
		return m.ownCallCreator()
	}
	return call.Peer()
}

func (m *Manager) ownCallCreator() types.JID {
	if m == nil || m.waClient == nil || m.waClient.Store == nil {
		return types.EmptyJID
	}
	return m.waClient.Store.GetLID()
}

func (m *Manager) endCallsFromMatrixRTC(waCallID string) int {
	m.mu.Lock()
	calls := make([]*meowcaller.Call, 0, len(m.calls))
	if waCallID != "" {
		if call := m.calls[waCallID]; call != nil {
			calls = append(calls, call)
		}
	} else {
		for _, call := range m.calls {
			calls = append(calls, call)
		}
	}
	m.mu.Unlock()

	var ended int
	for _, call := range calls {
		if call.State() == meowcaller.CallPhaseEnded {
			continue
		}
		var err error
		if call.State() == meowcaller.CallPhaseRinging {
			err = call.Reject()
		} else {
			err = call.Hangup()
		}
		if err != nil {
			m.log.Warn().
				Err(err).
				Str("call_id", call.ID()).
				Int("phase", int(call.State())).
				Msg("Failed to end WhatsApp VOIP call after MatrixRTC event")
			continue
		}
		ended++
	}
	return ended
}

func (m *Manager) clearLiveKitConnecting(waCallID string) {
	m.mu.Lock()
	delete(m.livekitConnecting, waCallID)
	m.mu.Unlock()
}
