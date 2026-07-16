package voip

import "time"

const maxPendingWhatsAppVideoFrames = 64

type LiveKitVideoFrame struct {
	AccessUnit []byte
	Duration   time.Duration
}

type whatsAppVideoStartupBuffer struct {
	ready  bool
	frames []LiveKitVideoFrame
}

func (b *whatsAppVideoStartupBuffer) Len() int {
	if b == nil {
		return 0
	}
	return len(b.frames)
}

func (b *whatsAppVideoStartupBuffer) Send(frame LiveKitVideoFrame, send func(LiveKitVideoFrame) error) (int, error) {
	if b == nil || len(frame.AccessUnit) == 0 || send == nil {
		return 0, nil
	}
	if b.ready {
		return 0, send(frame)
	}
	b.enqueue(frame)
	flushed := 0
	for len(b.frames) > 0 {
		if err := send(b.frames[0]); err != nil {
			return flushed, err
		}
		b.frames[0] = LiveKitVideoFrame{}
		b.frames = b.frames[1:]
		flushed++
	}
	b.ready = true
	return flushed, nil
}

func (b *whatsAppVideoStartupBuffer) enqueue(frame LiveKitVideoFrame) {
	queued := LiveKitVideoFrame{
		AccessUnit: append([]byte(nil), frame.AccessUnit...),
		Duration:   frame.Duration,
	}
	b.frames = append(b.frames, queued)
	if len(b.frames) <= maxPendingWhatsAppVideoFrames {
		return
	}
	copy(b.frames, b.frames[len(b.frames)-maxPendingWhatsAppVideoFrames:])
	b.frames = b.frames[:maxPendingWhatsAppVideoFrames]
}
