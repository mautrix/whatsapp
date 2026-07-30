package voip

import (
	"testing"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
)

type recordingGroupVideoSink struct {
	frames       [][]byte
	orientations []int
	muted        []bool
}

func (s *recordingGroupVideoSink) WriteVideo(frame []byte) error {
	s.frames = append(s.frames, append([]byte(nil), frame...))
	return nil
}

func (s *recordingGroupVideoSink) SetOrientation(orientation int) {
	s.orientations = append(s.orientations, orientation)
}

func (s *recordingGroupVideoSink) setMuted(muted bool) {
	s.muted = append(s.muted, muted)
}

func TestWhatsAppVideoRouterKeepsOneStableGroupCamera(t *testing.T) {
	camera := &recordingGroupVideoSink{}
	screen := &recordingGroupVideoSink{}
	router := newWhatsAppVideoRouter(camera, screen, camera.setMuted, screen.setMuted)
	alice := types.NewJID("111", types.DefaultUserServer)
	bob := types.NewJID("222", types.DefaultUserServer)
	router.SetGroupState(meowcaller.GroupCallState{
		Participants: []meowcaller.GroupCallParticipant{
			{JID: alice, State: "connected"},
			{JID: bob, State: "connected"},
		},
	})

	router.WriteParticipantFrame(meowcaller.ParticipantVideoFrame{
		ParticipantID: alice.String(),
		Sender:        alice,
		Orientation:   1,
		AccessUnit:    []byte{0x01},
	})
	router.WriteParticipantFrame(meowcaller.ParticipantVideoFrame{
		ParticipantID: bob.String(),
		Sender:        bob,
		Orientation:   2,
		AccessUnit:    []byte{0x02},
	})

	if len(camera.frames) != 1 || camera.frames[0][0] != 0x01 {
		t.Fatalf("camera frames = %v, want only the first connected participant", camera.frames)
	}
	if len(camera.orientations) != 1 || camera.orientations[0] != 1 {
		t.Fatalf("camera orientations = %v, want [1]", camera.orientations)
	}
}

func TestWhatsAppVideoRouterSeparatesScreenShareFromCamera(t *testing.T) {
	camera := &recordingGroupVideoSink{}
	screen := &recordingGroupVideoSink{}
	router := newWhatsAppVideoRouter(camera, screen, camera.setMuted, screen.setMuted)
	alice := types.NewJID("111", types.DefaultUserServer)
	router.SetScreenShare(meowcaller.ScreenShareState{Participant: alice, Active: true})

	router.WriteParticipantFrame(meowcaller.ParticipantVideoFrame{
		ParticipantID: alice.String(),
		Sender:        alice,
		Orientation:   3,
		AccessUnit:    []byte{0x03},
	})

	if len(camera.frames) != 0 {
		t.Fatalf("camera received screen-share frames: %v", camera.frames)
	}
	if len(screen.frames) != 1 || screen.frames[0][0] != 0x03 {
		t.Fatalf("screen frames = %v, want one screen-share frame", screen.frames)
	}
	if len(screen.orientations) != 1 || screen.orientations[0] != 3 {
		t.Fatalf("screen orientations = %v, want [3]", screen.orientations)
	}
	if len(screen.muted) == 0 || screen.muted[len(screen.muted)-1] {
		t.Fatalf("screen mute transitions = %v, want unmuted", screen.muted)
	}

	router.SetScreenShare(meowcaller.ScreenShareState{Participant: alice, Active: false})
	router.WriteParticipantFrame(meowcaller.ParticipantVideoFrame{
		ParticipantID: alice.String(),
		Sender:        alice,
		AccessUnit:    []byte{0x04},
	})
	if len(camera.frames) != 1 || camera.frames[0][0] != 0x04 {
		t.Fatalf("camera frames after screen-share stop = %v, want camera frame", camera.frames)
	}
	if !screen.muted[len(screen.muted)-1] {
		t.Fatalf("screen mute transitions = %v, want muted after stop", screen.muted)
	}
}

func TestMatrixVideoRouterForwardsOnlyTheActiveSource(t *testing.T) {
	var got []byte
	router := newMatrixVideoSourceRouter(func(frame LiveKitVideoFrame) error {
		got = append(got, frame.AccessUnit...)
		return nil
	})

	if err := router.WriteCamera(LiveKitVideoFrame{AccessUnit: []byte{0x01}}); err != nil {
		t.Fatal(err)
	}
	router.SetScreenSharing(true)
	if err := router.WriteCamera(LiveKitVideoFrame{AccessUnit: []byte{0x02}}); err != nil {
		t.Fatal(err)
	}
	if err := router.WriteScreen(LiveKitVideoFrame{AccessUnit: []byte{0x03}}); err != nil {
		t.Fatal(err)
	}
	router.SetScreenSharing(false)
	if err := router.WriteScreen(LiveKitVideoFrame{AccessUnit: []byte{0x04}}); err != nil {
		t.Fatal(err)
	}
	if err := router.WriteCamera(LiveKitVideoFrame{AccessUnit: []byte{0x05}}); err != nil {
		t.Fatal(err)
	}

	want := []byte{0x01, 0x03, 0x05}
	if string(got) != string(want) {
		t.Fatalf("forwarded frames = %v, want %v", got, want)
	}
}
