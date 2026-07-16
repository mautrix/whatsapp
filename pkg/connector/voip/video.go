package voip

import (
	"errors"
	"sync"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller"
)

var ErrVideoSinkClosed = errors.New("voip: video sink closed")

type LiveKitH264Writer struct {
	mu    sync.RWMutex
	track interface {
		WriteSample(media.Sample, *lksdk.SampleWriteOptions) error
	}
	duration time.Duration
	closed   bool
}

type liveKitVideoOrientationSetter interface {
	SetVideoOrientation(uint8)
}

func NewLiveKitH264Writer(track interface {
	WriteSample(media.Sample, *lksdk.SampleWriteOptions) error
}, duration time.Duration) *LiveKitH264Writer {
	if duration <= 0 {
		duration = time.Second / 30
	}
	return &LiveKitH264Writer{track: track, duration: duration}
}

func (w *LiveKitH264Writer) WriteVideo(accessUnit []byte) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return ErrVideoSinkClosed
	}
	if w.track == nil || len(accessUnit) == 0 {
		return nil
	}
	sample := media.Sample{
		Data:     append([]byte(nil), accessUnit...),
		Duration: w.duration,
	}
	return w.track.WriteSample(sample, nil)
}

func (w *LiveKitH264Writer) SetOrientation(orientation int) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return
	}
	setLiveKitVideoOrientation(w.track, orientation)
}

func setLiveKitVideoOrientation(track any, orientation int) bool {
	setter, ok := track.(liveKitVideoOrientationSetter)
	if !ok {
		return false
	}
	setter.SetVideoOrientation(uint8(orientation) & 0x03)
	return true
}

func (w *LiveKitH264Writer) Close() error {
	w.mu.Lock()
	w.closed = true
	w.track = nil
	w.mu.Unlock()
	return nil
}

var _ meowcaller.VideoSink = (*LiveKitH264Writer)(nil)
