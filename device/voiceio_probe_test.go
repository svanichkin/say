//go:build darwin

package device

import (
	"math"
	"os"
	"testing"
	"time"
)

// TestVoiceIOProbe opens the system voice unit at 8 kHz on a real device
// and checks that both directions run. It opens the microphone and plays a
// tone, so it runs only with SAY_DEVICE_TESTS=1 (not in CI, whose machines
// have no sound hardware).
func TestVoiceIOProbe(t *testing.T) {
	if os.Getenv("SAY_DEVICE_TESTS") == "" {
		t.Skip("set SAY_DEVICE_TESTS=1 to test the system voice unit on this machine's devices")
	}
	if !VoiceIOSupported {
		t.Skip("no system voice unit on this platform")
	}
	var inFrames, inBlocks, maxIn, inPeak int64
	vio, err := StartVoiceIO(8000, true)
	if err != nil {
		t.Skipf("voice unit unavailable: %v", err)
	}
	// The unit's thread only moves samples through its rings: a goroutine
	// fills the output with a quiet 440 Hz tone and empties the input.
	phase := 0.0
	tone := make([]int16, 80)
	buf := make([]int16, 1024)
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		for st := vio.State(); st.Written-st.Played < 1600; st.Written += 80 {
			for i := range tone {
				tone[i] = int16(500 * math.Sin(phase))
				phase += 2 * math.Pi * 440 / 8000
			}
			if vio.Write(tone) < len(tone) {
				break
			}
		}
		for {
			n, _ := vio.ReadMic(buf)
			if n == 0 {
				break
			}
			inBlocks++
			inFrames += int64(n)
			maxIn = max(maxIn, int64(n))
			for _, v := range buf[:n] {
				inPeak = max(inPeak, int64(math.Abs(float64(v))))
			}
		}
	}
	t.Logf("output latency %v", vio.Latency(false))
	t.Logf("input latency %v", vio.Latency(true))
	st := vio.State()
	vio.Close()
	t.Logf("2 s: input %d frames in %d blocks (max %d), output %d played in %d callbacks, %d dry; mic peak %d, %d mic samples dropped",
		inFrames, inBlocks, maxIn, st.Played, st.Callbacks, st.DryCallbacks, inPeak, st.MicDropped)
	outFrames := int64(st.Played)
	if inFrames < 8000 || outFrames < 8000 {
		t.Fatalf("too few frames: input %d, output %d (want ~16000 each)", inFrames, outFrames)
	}
}
