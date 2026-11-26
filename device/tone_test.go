//go:build !plan9

package device

import "testing"

// A ring starts quiet and rises to its full level, never back down.
func TestToneGainRises(t *testing.T) {
	rise := 1000
	prev := 0.0
	for i := 0; i <= rise+10; i += 10 {
		g := toneGain(i, rise)
		if g < prev || g > 1 {
			t.Fatalf("gain %v at %d after %v", g, i, prev)
		}
		prev = g
	}
	if toneGain(0, rise) > 0.1 || toneGain(rise, rise) != 1 {
		t.Fatalf("from %v to %v", toneGain(0, rise), toneGain(rise, rise))
	}
}
