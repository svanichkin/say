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
