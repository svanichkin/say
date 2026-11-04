package ui

import (
	"math/rand"
	"testing"
	"time"
)

func acceptedFPS(t *testing.T, interval, jitter time.Duration, seconds int) float64 {
	t.Helper()
	SetVideoFPSLimit(25)
	rng := rand.New(rand.NewSource(1))
	start := time.Now()
	accepted := 0
	n := int(time.Duration(seconds) * time.Second / interval)
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i)*interval + time.Duration(rng.Int63n(int64(2*jitter+1))) - jitter)
		if allowFrameAt("test", at) {
			accepted++
		}
	}
	return float64(accepted) / float64(seconds)
}

func TestFrameLimiterToleratesJitter(t *testing.T) {
	// Frames at the target 25 fps with ±6 ms of jitter must all pass.
	if fps := acceptedFPS(t, 40*time.Millisecond, 6*time.Millisecond, 20); fps < 24.5 {
		t.Fatalf("25 fps input with jitter accepted at %.1f fps", fps)
	}
	// A faster source is still limited to the target rate.
	if fps := acceptedFPS(t, 10*time.Millisecond, 0, 20); fps > 25.5 {
		t.Fatalf("100 fps input accepted at %.1f fps, want 25", fps)
	}
}
