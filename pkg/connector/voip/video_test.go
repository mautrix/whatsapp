package voip

import (
	"bytes"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller"
)

func annexBNAL(nalu ...byte) []byte {
	return append([]byte{0, 0, 0, 1}, nalu...)
}

func TestH264ParameterSetRepeaterAddsCachedHeadersToIDR(t *testing.T) {
	repeater := h264ParameterSetRepeater{}
	sps := annexBNAL(0x67, 0x42, 0xe0, 0x1f)
	pps := annexBNAL(0x68, 0xce, 0x06, 0xe2)
	repeater.Normalize(append(append([]byte{}, sps...), pps...))

	idr := annexBNAL(0x65, 0x88, 0x84)
	got, repeated := repeater.Normalize(idr)
	want := append(append(append([]byte{}, sps...), pps...), idr...)
	if !repeated {
		t.Fatal("Normalize did not report repeated parameter sets")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("normalized IDR = %x, want %x", got, want)
	}
}

func TestH264ParameterSetRepeaterPreservesCompleteIDR(t *testing.T) {
	repeater := h264ParameterSetRepeater{}
	au := append(append(annexBNAL(0x67, 0x42, 0xe0, 0x1f), annexBNAL(0x68, 0xce, 0x06, 0xe2)...), annexBNAL(0x65, 0x88, 0x84)...)

	got, repeated := repeater.Normalize(au)
	if repeated {
		t.Fatal("Normalize reported repeating already-present parameter sets")
	}
	if !bytes.Equal(got, au) {
		t.Fatalf("complete IDR changed: got %x, want %x", got, au)
	}
}

func TestH264ParameterSetRepeaterUsesCurrentAndCachedHeadersInDecodeOrder(t *testing.T) {
	repeater := h264ParameterSetRepeater{}
	oldSPS := annexBNAL(0x67, 0x42, 0xe0, 0x1f)
	pps := annexBNAL(0x68, 0xce, 0x06, 0xe2)
	repeater.Normalize(append(append([]byte{}, oldSPS...), pps...))

	newSPS := annexBNAL(0x67, 0x42, 0xe0, 0x20)
	idr := annexBNAL(0x65, 0x99)
	au := append(append([]byte{}, newSPS...), idr...)
	got, repeated := repeater.Normalize(au)
	want := append(append(append([]byte{}, newSPS...), pps...), idr...)
	if !repeated {
		t.Fatal("Normalize did not report filling the missing PPS")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("normalized partial IDR = %x, want %x", got, want)
	}
}

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
