package voip

import (
	"io"
	"math"
	"testing"

	lkmedia "github.com/livekit/media-sdk"
	"github.com/purpshell/meowcaller"
)

func TestFloat32FrameToPCM16ClipsAndScales(t *testing.T) {
	frame := []float32{-2, -1, -0.5, 0, 0.5, 1, 2}
	sample := Float32FrameToPCM16(frame)
	expected := lkmedia.PCM16Sample{math.MinInt16, math.MinInt16, -16384, 0, 16384, math.MaxInt16, math.MaxInt16}
	for i, value := range expected {
		if sample[i] != value {
			t.Fatalf("sample[%d] = %d, want %d", i, sample[i], value)
		}
	}
}

func TestMeowcallerAudioSourceFramesAndEOF(t *testing.T) {
	src := NewMeowcallerAudioSource(1)
	sample := make(lkmedia.PCM16Sample, meowcaller.FrameSamples)
	for i := range sample {
		sample[i] = int16(i)
	}
	if err := src.WriteSample(sample); err != nil {
		t.Fatalf("WriteSample returned error: %v", err)
	}
	frame, err := src.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}
	if len(frame) != meowcaller.FrameSamples {
		t.Fatalf("frame length = %d, want %d", len(frame), meowcaller.FrameSamples)
	}
	if frame[1] != float32(1)/32768 {
		t.Fatalf("frame[1] = %f, want %f", frame[1], float32(1)/32768)
	}
	if err = src.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	_, err = src.ReadFrame()
	if err != io.EOF {
		t.Fatalf("ReadFrame after close returned %v, want io.EOF", err)
	}
}
