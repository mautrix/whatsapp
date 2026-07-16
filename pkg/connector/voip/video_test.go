package voip

import (
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4/pkg/media"
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
