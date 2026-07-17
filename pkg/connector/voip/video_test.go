package voip

import (
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller"
)

type orientedSampleTrack struct {
	orientation uint8
}

func (t *orientedSampleTrack) WriteSample(media.Sample, *lksdk.SampleWriteOptions) error {
	return nil
}

func (t *orientedSampleTrack) SetVideoOrientation(orientation uint8) {
	t.orientation = orientation
}

func TestLiveKitH264WriterSetsVideoOrientation(t *testing.T) {
	track := &orientedSampleTrack{}
	writer := NewLiveKitH264Writer(track, time.Second/30)

	writer.SetOrientation(5)

	if track.orientation != 1 {
		t.Fatalf("track orientation = %d, want 1", track.orientation)
	}
}

func TestLiveKitParticipantRequestsRemoteVideoKeyframe(t *testing.T) {
	const wantSSRC = webrtc.SSRC(0x12345678)
	var gotSSRC webrtc.SSRC
	participant := &LiveKitParticipant{}

	if participant.requestRemoteVideoKeyframe() {
		t.Fatal("requestRemoteVideoKeyframe returned true before track subscription")
	}
	participant.setRemoteVideoPLI(func(ssrc webrtc.SSRC) {
		gotSSRC = ssrc
	}, wantSSRC)
	if gotSSRC != wantSSRC {
		t.Fatalf("deferred PLI SSRC = %#x, want %#x", gotSSRC, wantSSRC)
	}

	gotSSRC = 0
	if !participant.requestRemoteVideoKeyframe() {
		t.Fatal("requestRemoteVideoKeyframe returned false with a subscribed track")
	}
	if gotSSRC != wantSSRC {
		t.Fatalf("immediate PLI SSRC = %#x, want %#x", gotSSRC, wantSSRC)
	}
}

func TestManagerDefersVideoKeyframeOnlyForTrackedCall(t *testing.T) {
	manager := &Manager{
		calls:                make(map[string]*meowcaller.Call),
		livekit:              make(map[string]*LiveKitParticipant),
		videoKeyframePending: make(map[string]bool),
	}

	manager.requestLiveKitVideoKeyframe("ended")
	if manager.videoKeyframePending["ended"] {
		t.Fatal("keyframe request was retained for an untracked call")
	}

	manager.calls["active"] = &meowcaller.Call{}
	manager.requestLiveKitVideoKeyframe("active")
	if !manager.videoKeyframePending["active"] {
		t.Fatal("keyframe request was not retained for a tracked call")
	}
}
