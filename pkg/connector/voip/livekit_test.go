package voip

import (
	"testing"

	livekitproto "github.com/livekit/protocol/livekit"
)

func TestVideoTrackPublicationOptionsPreserveScreenShareSource(t *testing.T) {
	cfg := VideoConfig{MaxWidth: 1280, MaxHeight: 720}
	opts := videoTrackPublicationOptions("whatsapp-screen", livekitproto.TrackSource_SCREEN_SHARE, cfg)
	if opts.Name != "whatsapp-screen" {
		t.Fatalf("track name = %q, want whatsapp-screen", opts.Name)
	}
	if opts.Source != livekitproto.TrackSource_SCREEN_SHARE {
		t.Fatalf("track source = %s, want SCREEN_SHARE", opts.Source)
	}
	if opts.VideoWidth != 1280 || opts.VideoHeight != 720 {
		t.Fatalf("track dimensions = %dx%d, want 1280x720", opts.VideoWidth, opts.VideoHeight)
	}
}

func TestLiveKitVideoSourceClassifiesScreenShareIndependently(t *testing.T) {
	if liveKitVideoSourceRole(livekitproto.TrackSource_CAMERA) != liveKitVideoSourceCamera {
		t.Fatal("camera publication was not classified as camera")
	}
	if liveKitVideoSourceRole(livekitproto.TrackSource_SCREEN_SHARE) != liveKitVideoSourceScreenShare {
		t.Fatal("screen-share publication was not classified as screen share")
	}
}
