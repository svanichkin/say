package device

import (
	"math"
	"sync/atomic"
	"testing"
	"time"
)

// TestVoiceIOProbe opens the system voice unit at 8 kHz on a real device
// and checks that both directions run. Skipped where unsupported.
func TestVoiceIOProbe(t *testing.T) {
	if !VoiceIOSupported {
		t.Skip("no system voice unit on this platform")
	}
	var inFrames, outFrames, inCalls, outCalls, inPeak atomic.Int64
	var maxIn, maxOut atomic.Int64
	phase := 0.0
	vio, err := StartVoiceIO(8000, func(s []int16) {
		inCalls.Add(1)
		inFrames.Add(int64(len(s)))
		maxIn.Store(max(maxIn.Load(), int64(len(s))))
		for _, v := range s {
			inPeak.Store(max(inPeak.Load(), int64(math.Abs(float64(v)))))
		}
	}, func(out []int16) {
		outCalls.Add(1)
		outFrames.Add(int64(len(out)))
		maxOut.Store(max(maxOut.Load(), int64(len(out))))
		for i := range out { // a quiet 440 Hz tone
			out[i] = int16(500 * math.Sin(phase))
			phase += 2 * math.Pi * 440 / 8000
		}
	})
	if err != nil {
		t.Skipf("voice unit unavailable: %v", err)
	}
	time.Sleep(2 * time.Second)
	t.Logf("output latency %v", vio.Latency(false))
	t.Logf("input latency %v", vio.Latency(true))
	vio.Close()
	t.Logf("2 s: input %d frames in %d calls (max %d), output %d frames in %d calls (max %d), mic peak %d",
		inFrames.Load(), inCalls.Load(), maxIn.Load(), outFrames.Load(), outCalls.Load(), maxOut.Load(), inPeak.Load())
	if inFrames.Load() < 8000 || outFrames.Load() < 8000 {
		t.Fatalf("too few frames: input %d, output %d (want ~16000 each)", inFrames.Load(), outFrames.Load())
	}
}
