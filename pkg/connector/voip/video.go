package voip

import (
	"errors"
	"fmt"
	"sync"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller"
	wartp "github.com/purpshell/meowcaller/rtp"
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

type h264ParameterSetRepeater struct {
	sps []byte
	pps []byte
}

func (r *h264ParameterSetRepeater) Normalize(accessUnit []byte) ([]byte, bool) {
	nalus := wartp.SplitAnnexB(accessUnit)
	if len(nalus) == 0 {
		return accessUnit, false
	}
	var currentSPS, currentPPS []byte
	hasIDR := false
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1f {
		case 5:
			hasIDR = true
		case 7:
			currentSPS = nalu
			r.sps = append(r.sps[:0], nalu...)
		case 8:
			currentPPS = nalu
			r.pps = append(r.pps[:0], nalu...)
		}
	}
	if !hasIDR || (currentSPS != nil && currentPPS != nil) {
		return accessUnit, false
	}
	sps := currentSPS
	if sps == nil {
		sps = r.sps
	}
	pps := currentPPS
	if pps == nil {
		pps = r.pps
	}
	if len(sps) == 0 || len(pps) == 0 {
		return accessUnit, false
	}

	normalized := make([]byte, 0, len(accessUnit)+len(sps)+len(pps)+8)
	normalized = appendAnnexBNAL(normalized, sps)
	normalized = appendAnnexBNAL(normalized, pps)
	for _, nalu := range nalus {
		if len(nalu) == 0 || nalu[0]&0x1f == 7 || nalu[0]&0x1f == 8 {
			continue
		}
		normalized = appendAnnexBNAL(normalized, nalu)
	}
	return normalized, true
}

func appendAnnexBNAL(dst, nalu []byte) []byte {
	dst = append(dst, 0, 0, 0, 1)
	return append(dst, nalu...)
}

func h264AccessUnitMetadata(accessUnit []byte) (nalTypes []int, profileLevelID string, hasIDR, hasSPS, hasPPS bool) {
	for _, nalu := range wartp.SplitAnnexB(accessUnit) {
		if len(nalu) == 0 {
			continue
		}
		nalType := int(nalu[0] & 0x1f)
		nalTypes = append(nalTypes, nalType)
		switch nalType {
		case 5:
			hasIDR = true
		case 7:
			hasSPS = true
			if len(nalu) >= 4 {
				profileLevelID = fmt.Sprintf("%02x%02x%02x", nalu[1], nalu[2], nalu[3])
			}
		case 8:
			hasPPS = true
		}
	}
	return
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
