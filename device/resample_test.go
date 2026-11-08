package device

import (
	"math"
	"testing"
)

func sine(freq float64, rate, n int, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

// rms skips the first skip samples (filter warm-up).
func rms(x []float32, skip int) float64 {
	var s float64
	for _, v := range x[skip:] {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)-skip))
}

func TestRateConverterLength(t *testing.T) {
	for _, r := range [][2]int{{8000, 44100}, {44100, 8000}, {8000, 48000}, {48000, 8000}, {8000, 8000}} {
		c := newRateConverter(r[0], r[1])
		var out []float32
		// Odd block sizes: the converter must carry its phase across calls.
		in := make([]float32, 37)
		for range 1000 {
			out = c.convert(out, in)
		}
		want := 37 * 1000 * r[1] / r[0]
		if d := len(out) - want; d < -1 || d > 1 {
			t.Errorf("%d -> %d Hz: %d samples, want %d", r[0], r[1], len(out), want)
		}
	}
}

func TestRateConverterVoiceBand(t *testing.T) {
	// 1 kHz passes in both directions with its level. Going up, linear
	// interpolation droops by sinc²(f/8 kHz): -0.45 dB at 1 kHz (miniaudio's
	// default linear resampler does the same).
	up := newRateConverter(8000, 44100).convert(nil, sine(1000, 8000, 8000, 10000))
	droop := math.Pow(math.Sin(math.Pi/8)/(math.Pi/8), 2)
	if got, want := rms(up, 2000), droop*10000/math.Sqrt2; math.Abs(got-want) > 100 {
		t.Errorf("8 -> 44.1 kHz: 1 kHz rms %.0f, want %.0f", got, want)
	}
	down := newRateConverter(44100, 8000).convert(nil, sine(1000, 44100, 44100, 10000))
	if got := rms(down, 400); math.Abs(got-10000/math.Sqrt2) > 300 {
		t.Errorf("44.1 -> 8 kHz: 1 kHz rms %.0f, want %.0f", got, 10000/math.Sqrt2)
	}
}

func TestRateConverterRejectsAliases(t *testing.T) {
	// 6 kHz cannot be represented at 8 kHz: it must not fold back into the
	// voice band (as 2 kHz).
	down := newRateConverter(44100, 8000).convert(nil, sine(6000, 44100, 44100, 10000))
	if got := rms(down, 400); got > 10000/math.Sqrt2/100 {
		t.Errorf("6 kHz alias at 8 kHz: rms %.0f, want under 1%%", got)
	}
	// Upsampling 3 kHz must not leave its image at 5 kHz: the output is the
	// tone alone, so its level stays that of the tone.
	up := newRateConverter(8000, 44100).convert(nil, sine(3000, 8000, 8000, 10000))
	ref := sine(3000, 44100, len(up), 1)
	// Project out the 3 kHz tone (any phase) and measure the rest.
	var ss, cc, sc, cs float64
	for i, v := range up[2000:] {
		s := float64(ref[i+2000])
		c := math.Cos(2 * math.Pi * 3000 * float64(i+2000) / 44100)
		ss += float64(v) * s
		cc += float64(v) * c
		sc += s * s
		cs += c * c
	}
	a, b := ss/sc, cc/cs
	var rest float64
	for i, v := range up[2000:] {
		s := float64(ref[i+2000])
		c := math.Cos(2 * math.Pi * 3000 * float64(i+2000) / 44100)
		d := float64(v) - a*s - b*c
		rest += d * d
	}
	rest = math.Sqrt(rest / float64(len(up)-2000))
	if rest > 10000/math.Sqrt2/30 {
		t.Errorf("8 -> 44.1 kHz: 3 kHz image rms %.0f, want under ~3%%", rest)
	}
}

func TestClampInt16(t *testing.T) {
	for in, want := range map[float32]int16{40000: 32767, -40000: -32768, 1.6: 2, -1.6: -2} {
		if got := clampInt16(in); got != want {
			t.Errorf("clampInt16(%v) = %d, want %d", in, got, want)
		}
	}
}
