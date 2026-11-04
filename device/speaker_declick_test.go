package device

import (
	"math"
	"testing"
)

func maxStep(s []int16) int {
	m := 0
	for i := 1; i < len(s); i++ {
		if d := int(math.Abs(float64(s[i]) - float64(s[i-1]))); d > m {
			m = d
		}
	}
	return m
}

func TestCrossfadeSmoothsDiscontinuities(t *testing.T) {
	// The speaker was at +20000 when the next frame starts at -20000.
	var c crossfade
	c.start(20000)
	frame := make([]int16, 160)
	for i := range frame {
		frame[i] = -20000
	}
	c.apply(frame)
	if step := maxStep(append([]int16{20000}, frame...)); step > 1500 {
		t.Fatalf("crossfade still steps by %d", step)
	}
	if frame[len(frame)-1] != -20000 {
		t.Fatalf("crossfade altered the signal after the ramp: %d", frame[len(frame)-1])
	}

	// An underrun at +20000 ramps down to silence instead of stepping to 0.
	out := make([]int16, 160)
	c.fadeOut(out, 20000)
	if step := maxStep(append([]int16{20000}, out...)); step > 1500 {
		t.Fatalf("fade-out steps by %d", step)
	}
	if out[len(out)-1] != 0 {
		t.Fatalf("fade-out does not reach silence: %d", out[len(out)-1])
	}
}

func TestStretchedFrameUsesOriginalMediaDuration(t *testing.T) {
	for _, length := range []int{152, 160, 168} {
		if got := mediaSamplePosition(length, length, 160); got != 160 {
			t.Fatalf("%d PCM samples advanced media by %d", length, got)
		}
		if got := mediaSamplePosition(length/2, length, 160); got != 80 {
			t.Fatalf("middle of stretched frame: %d", got)
		}
	}
}
