package voip

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	lkmedia "github.com/livekit/media-sdk"
	"github.com/purpshell/meowcaller"
)

var ErrAudioSourceClosed = errors.New("voip: audio source closed")

func Float32FrameToPCM16(frame []float32) lkmedia.PCM16Sample {
	sample := make(lkmedia.PCM16Sample, len(frame))
	for i, value := range frame {
		switch {
		case value > 1:
			value = 1
		case value < -1:
			value = -1
		}
		if value == 1 {
			sample[i] = math.MaxInt16
		} else {
			sample[i] = int16(value * 32768)
		}
	}
	return sample
}

func PCM16ToFloat32Frame(sample lkmedia.PCM16Sample) []float32 {
	frame := make([]float32, len(sample))
	for i, value := range sample {
		frame[i] = float32(value) / 32768
	}
	return frame
}

type LiveKitPCMWriter struct {
	mu    sync.RWMutex
	track interface {
		WriteSample(lkmedia.PCM16Sample) error
	}
	closed bool
}

func NewLiveKitPCMWriter(track interface {
	WriteSample(lkmedia.PCM16Sample) error
}) *LiveKitPCMWriter {
	return &LiveKitPCMWriter{track: track}
}

func (w *LiveKitPCMWriter) WriteFrame(frame []float32) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return ErrAudioSourceClosed
	}
	if w.track == nil {
		return nil
	}
	return w.track.WriteSample(Float32FrameToPCM16(frame))
}

func (w *LiveKitPCMWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	w.track = nil
	w.mu.Unlock()
	return nil
}

type MeowcallerAudioSource struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queue   []float32
	closed  bool
	maxSize int
}

func NewMeowcallerAudioSource(maxFrames int) *MeowcallerAudioSource {
	if maxFrames <= 0 {
		maxFrames = 8
	}
	src := &MeowcallerAudioSource{
		maxSize: maxFrames * meowcaller.FrameSamples,
	}
	src.cond = sync.NewCond(&src.mu)
	return src
}

func (src *MeowcallerAudioSource) WriteSample(sample lkmedia.PCM16Sample) error {
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.closed {
		return ErrAudioSourceClosed
	}
	frame := PCM16ToFloat32Frame(sample)
	src.queue = append(src.queue, frame...)
	if len(src.queue) > src.maxSize {
		copy(src.queue, src.queue[len(src.queue)-src.maxSize:])
		src.queue = src.queue[:src.maxSize]
	}
	src.cond.Signal()
	return nil
}

func (src *MeowcallerAudioSource) SampleRate() int {
	return meowcaller.SampleRate
}

func (src *MeowcallerAudioSource) String() string {
	return fmt.Sprintf("MeowcallerAudioSource(%d)", meowcaller.SampleRate)
}

func (src *MeowcallerAudioSource) ReadFrame() ([]float32, error) {
	src.mu.Lock()
	defer src.mu.Unlock()
	for len(src.queue) < meowcaller.FrameSamples && !src.closed {
		src.cond.Wait()
	}
	if len(src.queue) == 0 && src.closed {
		return nil, io.EOF
	}
	frame := make([]float32, meowcaller.FrameSamples)
	n := copy(frame, src.queue)
	if n == len(src.queue) {
		src.queue = src.queue[:0]
	} else {
		copy(src.queue, src.queue[n:])
		src.queue = src.queue[:len(src.queue)-n]
	}
	return frame, nil
}

func (src *MeowcallerAudioSource) Close() error {
	src.mu.Lock()
	src.closed = true
	src.queue = nil
	src.cond.Broadcast()
	src.mu.Unlock()
	return nil
}

var (
	_ meowcaller.AudioSink   = (*LiveKitPCMWriter)(nil)
	_ meowcaller.AudioSource = (*MeowcallerAudioSource)(nil)
)
