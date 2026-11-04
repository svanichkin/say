package device

import (
	"math"
	"math/rand"
	"testing"
)

func TestTemporalDenoiser(t *testing.T) {
	const w, h = 64, 48
	rng := rand.New(rand.NewSource(1))
	base := make([]byte, w*h*3)
	for i := range base {
		base[i] = byte(60 + i%120)
	}
	var d temporalDenoiser
	var rawVar, outVar float64
	var prevRaw, prevOut []byte
	for f := 0; f < 60; f++ { // static scene with +-6 sensor noise
		raw := make([]byte, len(base))
		for i := range raw {
			raw[i] = byte(int(base[i]) + rng.Intn(13) - 6)
		}
		out := append([]byte(nil), raw...)
		d.apply(out, w, h)
		if f > 10 {
			for i := range raw {
				rawVar += math.Abs(float64(raw[i]) - float64(prevRaw[i]))
				outVar += math.Abs(float64(out[i]) - float64(prevOut[i]))
			}
		}
		prevRaw, prevOut = raw, out
	}
	t.Logf("frame-to-frame change on a static scene: raw %.2f, filtered %.2f per channel",
		rawVar/float64(49*len(base)), outVar/float64(49*len(base)))
	if outVar > rawVar/3 {
		t.Fatalf("noise not reduced enough: %.0f vs %.0f", outVar, rawVar)
	}

	// Motion passes through unchanged in the very next frame.
	moved := make([]byte, len(base))
	for i := range moved {
		moved[i] = byte(min(255, int(base[i])+50)) // a clear change, far above the noise threshold
	}
	out := append([]byte(nil), moved...)
	d.apply(out, w, h)
	for i := range out {
		if out[i] != moved[i] {
			t.Fatalf("motion blurred at byte %d: %d, want %d", i, out[i], moved[i])
		}
	}
}
