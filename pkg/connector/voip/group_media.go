package voip

import (
	"sync"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
)

type whatsAppVideoSink interface {
	WriteVideo([]byte) error
	SetOrientation(int)
}

type whatsAppVideoRouter struct {
	mu sync.Mutex

	camera        whatsAppVideoSink
	screen        whatsAppVideoSink
	setCameraMute func(bool)
	setScreenMute func(bool)

	group          bool
	connected      map[string]struct{}
	screenSharers  map[string]struct{}
	selectedCamera string
	selectedScreen string
}

func newWhatsAppVideoRouter(
	camera, screen whatsAppVideoSink,
	setCameraMute, setScreenMute func(bool),
) *whatsAppVideoRouter {
	return &whatsAppVideoRouter{
		camera:        camera,
		screen:        screen,
		setCameraMute: setCameraMute,
		setScreenMute: setScreenMute,
		connected:     make(map[string]struct{}),
		screenSharers: make(map[string]struct{}),
	}
}

func (r *whatsAppVideoRouter) SetGroupState(state meowcaller.GroupCallState) {
	if r == nil {
		return
	}
	connected := make(map[string]struct{})
	for _, participant := range state.Participants {
		if participant.State != "connected" {
			continue
		}
		addVideoParticipantIdentity(connected, participant.JID)
		addVideoParticipantIdentity(connected, participant.PN)
		for _, device := range participant.Devices {
			addVideoParticipantIdentity(connected, device.JID)
		}
	}

	r.mu.Lock()
	r.group = true
	r.connected = connected
	cameraRemoved := r.selectedCamera != ""
	if cameraRemoved {
		_, cameraRemoved = connected[r.selectedCamera]
		cameraRemoved = !cameraRemoved
	}
	if cameraRemoved {
		r.selectedCamera = ""
	}
	screenRemoved := r.selectedScreen != ""
	if screenRemoved {
		_, screenRemoved = connected[r.selectedScreen]
		screenRemoved = !screenRemoved
	}
	if screenRemoved {
		delete(r.screenSharers, r.selectedScreen)
		r.selectedScreen = ""
	}
	setCameraMute := r.setCameraMute
	setScreenMute := r.setScreenMute
	r.mu.Unlock()

	if cameraRemoved && setCameraMute != nil {
		setCameraMute(true)
	}
	if screenRemoved && setScreenMute != nil {
		setScreenMute(true)
	}
}

func (r *whatsAppVideoRouter) SetScreenShare(state meowcaller.ScreenShareState) {
	if r == nil || state.Participant.IsEmpty() {
		return
	}
	participant := videoParticipantIdentity(state.Participant)
	r.mu.Lock()
	if state.Active {
		r.screenSharers[participant] = struct{}{}
		if r.selectedScreen == "" {
			r.selectedScreen = participant
		}
	} else {
		delete(r.screenSharers, participant)
		if r.selectedScreen == participant {
			r.selectedScreen = ""
		}
	}
	selected := r.selectedScreen
	setScreenMute := r.setScreenMute
	r.mu.Unlock()

	if setScreenMute != nil {
		setScreenMute(selected == "")
	}
}

func (r *whatsAppVideoRouter) WriteParticipantFrame(frame meowcaller.ParticipantVideoFrame) {
	if r == nil || len(frame.AccessUnit) == 0 {
		return
	}
	identity := participantVideoFrameIdentity(frame)
	r.mu.Lock()
	_, sharing := r.screenSharers[identity]
	if sharing {
		if r.selectedScreen == "" {
			r.selectedScreen = identity
		}
		if r.selectedScreen != identity {
			r.mu.Unlock()
			return
		}
		sink := r.screen
		setMuted := r.setScreenMute
		r.mu.Unlock()
		if setMuted != nil {
			setMuted(false)
		}
		writeWhatsAppVideoFrame(sink, frame)
		return
	}

	if r.group {
		if r.selectedCamera == "" {
			r.selectedCamera = identity
		}
		if r.selectedCamera != identity {
			r.mu.Unlock()
			return
		}
	}
	sink := r.camera
	setMuted := r.setCameraMute
	r.mu.Unlock()
	if setMuted != nil {
		setMuted(false)
	}
	writeWhatsAppVideoFrame(sink, frame)
}

func writeWhatsAppVideoFrame(sink whatsAppVideoSink, frame meowcaller.ParticipantVideoFrame) {
	if sink == nil {
		return
	}
	sink.SetOrientation(frame.Orientation)
	_ = sink.WriteVideo(frame.AccessUnit)
}

func participantVideoFrameIdentity(frame meowcaller.ParticipantVideoFrame) string {
	if !frame.Sender.IsEmpty() {
		return videoParticipantIdentity(frame.Sender)
	}
	if !frame.Device.IsEmpty() {
		return videoParticipantIdentity(frame.Device)
	}
	return frame.ParticipantID
}

func addVideoParticipantIdentity(target map[string]struct{}, jid types.JID) {
	if !jid.IsEmpty() {
		target[videoParticipantIdentity(jid)] = struct{}{}
	}
}

func videoParticipantIdentity(jid types.JID) string {
	return jid.ToNonAD().String()
}

type matrixVideoSourceRouter struct {
	mu            sync.RWMutex
	screenSharing bool
	write         func(LiveKitVideoFrame) error
}

func newMatrixVideoSourceRouter(write func(LiveKitVideoFrame) error) *matrixVideoSourceRouter {
	return &matrixVideoSourceRouter{write: write}
}

func (r *matrixVideoSourceRouter) SetScreenSharing(active bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.screenSharing = active
	r.mu.Unlock()
}

func (r *matrixVideoSourceRouter) ScreenSharing() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.screenSharing
}

func (r *matrixVideoSourceRouter) WriteCamera(frame LiveKitVideoFrame) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	screenSharing := r.screenSharing
	write := r.write
	r.mu.RUnlock()
	if screenSharing || write == nil {
		return nil
	}
	return write(frame)
}

func (r *matrixVideoSourceRouter) WriteScreen(frame LiveKitVideoFrame) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	screenSharing := r.screenSharing
	write := r.write
	r.mu.RUnlock()
	if !screenSharing || write == nil {
		return nil
	}
	return write(frame)
}
