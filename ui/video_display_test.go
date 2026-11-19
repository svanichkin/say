package ui

import "testing"

func TestVideoDisplayGammaLiftsMidtonesWithoutChangingChroma(t *testing.T) {
	oldFilter := activeColorFilter.Load()
	activeColorFilter.Store(nil)
	defer activeColorFilter.Store(oldFilter)

	for _, tc := range []struct{ y, want byte }{{0, 0}, {255, 255}} {
		r, g, b := FrameRGB(tc.y, 128, 128)
		if r != tc.want || g != tc.want || b != tc.want {
			t.Fatalf("neutral Y=%d displays as (%d,%d,%d)", tc.y, r, g, b)
		}
	}
	r, g, b := FrameRGB(64, 128, 128)
	if r <= 64 || r >= 80 || r != g || g != b {
		t.Fatalf("dark neutral midtone displays as (%d,%d,%d), want a gentle lift", r, g, b)
	}
	if got := FrameY(64); got != r {
		t.Fatalf("monochrome gamma = %d, colour gamma = %d", got, r)
	}

	baseR, baseG, baseB := ycbcrToRGB(100, 150, 140)
	r, g, b = FrameRGB(100, 150, 140)
	if r <= byte(baseR) || int(r)-baseR != int(g)-baseG || int(g)-baseG != int(b)-baseB {
		t.Fatalf("gamma changed chroma: before (%d,%d,%d), after (%d,%d,%d)", baseR, baseG, baseB, r, g, b)
	}
}
