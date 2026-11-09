package device

import (
	"testing"
	"time"
)

func TestCaptureGap(t *testing.T) {
	epoch := time.Unix(1000, 0)
	const rate = 8000
	at := func(samples int, extra time.Duration) time.Time {
		return epoch.Add(time.Duration(samples)*time.Second/rate + extra)
	}
	// A 160-sample block that ends where the sample count says: no gap.
	if g := captureGap(epoch, 800, 160, rate, at(960, 0)); g != 0 {
		t.Fatalf("on time: gap %d", g)
	}
	// Callback jitter well inside the tolerance.
	if g := captureGap(epoch, 800, 160, rate, at(960, 40*time.Millisecond)); g != 0 {
		t.Fatalf("40 ms late: gap %d", g)
	}
	// The device paused for a second.
	if g := captureGap(epoch, 800, 160, rate, at(960, time.Second)); g != rate {
		t.Fatalf("1 s pause: gap %d, want %d", g, rate)
	}
	// Running ahead of the wall clock is not a gap.
	if g := captureGap(epoch, 800, 160, rate, at(960, -time.Second)); g != 0 {
		t.Fatalf("ahead: gap %d", g)
	}
	if g := captureGap(time.Time{}, 0, 160, rate, at(160, time.Hour)); g != 0 {
		t.Fatalf("before the first block: gap %d", g)
	}
}
