package device

import (
	"testing"
	"time"
)

// With an 80 ms device period the callback position jumps by 20 ticks; the
// interpolated position must advance smoothly in between and never run past
// the next callback's position.
func TestPlaybackPositionAdvancesBetweenCallbacks(t *testing.T) {
	const tick = 4 * time.Millisecond
	const period, latency = 20, 80
	prev := playbackPosition(1000, 0, tick, period, latency)
	for ms := 4; ms <= 120; ms += 4 {
		got := playbackPosition(1000, time.Duration(ms)*time.Millisecond, tick, period, latency)
		want := uint16(1000 + min(ms/4, period) - latency)
		if got != want {
			t.Fatalf("after %d ms: position %d, want %d", ms, got, want)
		}
		if got < prev {
			t.Fatalf("position went back at %d ms", ms)
		}
		prev = got
	}
	// The next callback starts 20 ticks later: continuous with the cap.
	if next := playbackPosition(1020, 0, tick, period, latency); next != prev {
		t.Fatalf("jump at the next callback: %d -> %d", prev, next)
	}
}

func TestMonotonicPosition(t *testing.T) {
	if got := monotonicPosition(1000, 997, 20); got != 1000 {
		t.Fatalf("small step back passed: %d", got)
	}
	if got := monotonicPosition(1000, 1005, 20); got != 1005 {
		t.Fatalf("forward step blocked: %d", got)
	}
	if got := monotonicPosition(1000, 500, 20); got != 500 {
		t.Fatalf("resynchronisation blocked: %d", got)
	}
	if got := monotonicPosition(0x7ffe, 3, 20); got != 3 {
		t.Fatalf("wrap-around forward blocked: %d", got)
	}
}
