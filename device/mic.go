package device

import "time"

// AudioCaptureFrame is a microphone block and the monotonic time at which
// capture delivered its first sample.
type AudioCaptureFrame struct {
	Samples    []int16
	CapturedAt time.Time
	// FirstSample is counted before enqueueing, including discarded blocks.
	FirstSample uint64
}

// captureMaxLag is how far a capture's sample clock may fall behind the
// wall clock before the missing time counts as dropped samples.
const captureMaxLag = 100 * time.Millisecond

// captureGap reports how many samples a capture device failed to deliver:
// capture timestamps count samples from the first callback, so when the
// device pauses (macOS reconfiguring the audio hardware, the voice unit
// stalling), the stamps fall behind real time for good, and a receiver that
// shows video in sync with the sound holds every video frame that long. A
// block of block samples that arrives at now should have started at sample
// next, counted from epoch; if it started more than captureMaxLag later,
// the difference is returned, to be skipped as a gap in the timeline.
func captureGap(epoch time.Time, next uint64, block, rate int, now time.Time) uint64 {
	if epoch.IsZero() || rate <= 0 {
		return 0
	}
	started := now.Add(-time.Duration(block) * time.Second / time.Duration(rate))
	expected := epoch.Add(time.Duration(next) * time.Second / time.Duration(rate))
	lag := started.Sub(expected)
	if lag <= captureMaxLag {
		return 0
	}
	return uint64(lag * time.Duration(rate) / time.Second)
}

func bytesToInt16Mic(b []byte) []int16 {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(b[2*i]) | int16(b[2*i+1])<<8
	}
	return out
}
