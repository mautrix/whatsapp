package voip

import (
	"errors"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/signaling"
	"go.mau.fi/whatsmeow/types"
)

func TestBuildLocalMuteV2Node(t *testing.T) {
	peer := types.NewJID("12345", types.HiddenUserServer)
	callCreator := types.NewJID("67890", types.HiddenUserServer)
	node := buildLocalMuteV2Node("call-id", peer, callCreator, "wrapper-id", localUnmuteState)
	if node.Tag != "call" {
		t.Fatalf("node tag = %q, want call", node.Tag)
	}
	if got := node.AttrGetter().JID("to"); got != peer {
		t.Fatalf("to = %s, want %s", got, peer)
	}
	if got := node.AttrGetter().String("id"); got != "wrapper-id" {
		t.Fatalf("wrapper id = %q, want wrapper-id", got)
	}
	children := node.GetChildren()
	if len(children) != 1 {
		t.Fatalf("children = %d, want 1", len(children))
	}
	mute := children[0]
	if mute.Tag != "mute_v2" {
		t.Fatalf("child tag = %q, want mute_v2", mute.Tag)
	}
	attrs := mute.AttrGetter()
	if got := attrs.String("call-id"); got != "call-id" {
		t.Fatalf("call-id = %q, want call-id", got)
	}
	if got := attrs.JID("call-creator"); got != callCreator {
		t.Fatalf("call-creator = %s, want %s", got, callCreator)
	}
	if got := attrs.String("mute-state"); got != localUnmuteState {
		t.Fatalf("mute-state = %q, want %q", got, localUnmuteState)
	}
}

func TestLocalMuteStateFor(t *testing.T) {
	if got := localMuteStateFor(false); got != localUnmuteState {
		t.Fatalf("unmuted state = %q, want %q", got, localUnmuteState)
	}
	if got := localMuteStateFor(true); got != localMuteState {
		t.Fatalf("muted state = %q, want %q", got, localMuteState)
	}
}

func TestMatrixVideoActionFor(t *testing.T) {
	tests := []struct {
		muted, sending, receiving bool
		want                      matrixVideoAction
	}{
		{muted: true, sending: true, receiving: true, want: matrixVideoDisable},
		{muted: true, sending: false, receiving: true, want: matrixVideoNone},
		{muted: false, sending: true, receiving: false, want: matrixVideoEnable},
		{muted: false, sending: false, receiving: true, want: matrixVideoEnable},
		{muted: false, sending: false, receiving: false, want: matrixVideoUpgrade},
	}
	for _, tc := range tests {
		if got := matrixVideoActionFor(tc.muted, tc.sending, tc.receiving); got != tc.want {
			t.Errorf("muted:%v sending:%v receiving:%v => %d, want %d",
				tc.muted, tc.sending, tc.receiving, got, tc.want)
		}
	}
}

func TestRemoteVideoMuteForStateOnlyChangesPeerOwnedFlow(t *testing.T) {
	tests := []struct {
		state   int
		muted   bool
		changed bool
	}{
		{signaling.VideoStateEnabled, false, true},
		{signaling.VideoStateDisabled, true, true},
		{signaling.VideoStateStopped, true, true},
		{signaling.VideoStateUpgradeRequestV2, false, false},
		{signaling.VideoStateUpgradeAccept, false, false},
		{signaling.VideoStateUpgradeReject, false, false},
		{signaling.VideoStateUpgradeCancel, false, false},
	}
	for _, tc := range tests {
		muted, changed := remoteVideoMuteForState(meowcaller.VideoState{Raw: tc.state})
		if muted != tc.muted || changed != tc.changed {
			t.Errorf("state %d => muted:%v changed:%v, want muted:%v changed:%v",
				tc.state, muted, changed, tc.muted, tc.changed)
		}
	}
}

func TestWhatsAppVideoStartupBufferRetriesEarlyFrames(t *testing.T) {
	notReady := errors.New("meowcaller: call has no active video media")
	var attempts int
	var sent [][]byte
	var durations []time.Duration
	buffer := whatsAppVideoStartupBuffer{}

	flushed, err := buffer.Send(LiveKitVideoFrame{AccessUnit: []byte{1}, Duration: 33 * time.Millisecond}, func(frame LiveKitVideoFrame) error {
		attempts++
		return notReady
	})
	if err != notReady {
		t.Fatalf("first send error = %v, want notReady", err)
	}
	if flushed != 0 || buffer.Len() != 1 {
		t.Fatalf("flushed=%d buffered=%d, want flushed=0 buffered=1", flushed, buffer.Len())
	}

	flushed, err = buffer.Send(LiveKitVideoFrame{AccessUnit: []byte{2}, Duration: 17 * time.Millisecond}, func(frame LiveKitVideoFrame) error {
		attempts++
		sent = append(sent, append([]byte(nil), frame.AccessUnit...))
		durations = append(durations, frame.Duration)
		return nil
	})
	if err != nil {
		t.Fatalf("second send returned error: %v", err)
	}
	if flushed != 2 || buffer.Len() != 0 {
		t.Fatalf("flushed=%d buffered=%d, want flushed=2 buffered=0", flushed, buffer.Len())
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d, want 3", attempts)
	}
	if len(sent) != 2 || sent[0][0] != 1 || sent[1][0] != 2 {
		t.Fatalf("sent frames = %#v, want [1] then [2]", sent)
	}
	if len(durations) != 2 || durations[0] != 33*time.Millisecond || durations[1] != 17*time.Millisecond {
		t.Fatalf("sent durations = %v, want 33ms then 17ms", durations)
	}
	if got := buffer.ready; !got {
		t.Fatalf("buffer ready = %v, want true", got)
	}
}

func TestWhatsAppVideoStartupBufferCapsPendingFrames(t *testing.T) {
	notReady := errors.New("not ready")
	buffer := whatsAppVideoStartupBuffer{}
	for i := 0; i < maxPendingWhatsAppVideoFrames+3; i++ {
		_, _ = buffer.Send(LiveKitVideoFrame{AccessUnit: []byte{byte(i)}}, func(LiveKitVideoFrame) error {
			return notReady
		})
	}
	if buffer.Len() != maxPendingWhatsAppVideoFrames {
		t.Fatalf("buffered frames = %d, want %d", buffer.Len(), maxPendingWhatsAppVideoFrames)
	}
	if got := buffer.frames[0].AccessUnit[0]; got != 3 {
		t.Fatalf("oldest retained frame = %d, want 3", got)
	}
}
