package udp

import (
	"math"
	"math/rand"
	"testing"
)

// voice is a voiced sound at f0 Hz: a few harmonics falling off, as a vowel
// has, from sample from on.
func voice(f0 float64, from, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		t := float64(from+i) / audioSampleRate
		var v float64
		for h := 1; h <= 6; h++ {
			v += math.Sin(2*math.Pi*f0*float64(h)*t+float64(h)) / float64(h)
		}
		out[i] = int16(4000 * v)
	}
	return out
}

// period is the pitch period of s in samples, by autocorrelation.
func period(s []int16) int {
	best, bestR := 0, -1.0
	for p := accelMinPeriod; p <= accelMaxPeriod && p < len(s)/2; p++ {
		var c, e0, e1 float64
		for i := p; i < len(s); i++ {
			c += float64(s[i]) * float64(s[i-p])
			e0 += float64(s[i]) * float64(s[i])
			e1 += float64(s[i-p]) * float64(s[i-p])
		}
		if r := c / math.Sqrt(e0*e1); r > bestR+1e-9 {
			best, bestR = p, r
		}
	}
	return best
}

// maxStep is the largest jump between neighbouring samples: a click shows
// as one far over the signal's own.
func maxStep(s []int16) float64 {
	m := 0.0
	for i := 1; i < len(s); i++ {
		m = max(m, math.Abs(float64(s[i])-float64(s[i-1])))
	}
	return m
}

// A voiced frame, high or low, loses whole pitch periods: the voice keeps
// its pitch and the join does not click.
func TestAccelerateCutsAPitchPeriod(t *testing.T) {
	const frame = 160
	for _, f0 := range []float64{80, 110, 150, 220, 300} {
		hist := voice(f0, 0, accelMaxPeriod)
		in := voice(f0, accelMaxPeriod, frame)
		out, cut := accelerate(hist, in, accelMaxPeriod)
		if cut == 0 {
			t.Errorf("%v Hz: nothing cut from a voiced frame", f0)
			continue
		}
		if len(out) != frame-cut || out[0] != in[0] {
			t.Errorf("%v Hz: %d samples out after cutting %d, first %d (was %d)", f0, len(out), cut, out[0], in[0])
		}
		// The cut is a whole number of periods.
		p := audioSampleRate / f0
		if k := math.Round(float64(cut) / p); k < 1 || math.Abs(float64(cut)-k*p) > 1 {
			t.Errorf("%v Hz: cut %d samples, not a multiple of the %.1f-sample period", f0, cut, p)
		}
		// The pitch is the same after: hist, the shortened frame, and the
		// voice going on from where the frame ended.
		played := append(append(append([]int16(nil), hist...), out...), voice(f0, accelMaxPeriod+frame, frame)...)
		// (A period that is not a whole number of samples is found as a
		// multiple that nearly is.)
		if got := float64(period(played)); math.Abs(got-math.Round(got/p)*p) > 1 {
			t.Errorf("%v Hz: pitch period %.0f samples after the cut, want a multiple of %.1f", f0, got, p)
		}
		if s, ref := maxStep(played), maxStep(voice(f0, 0, 3*frame)); s > ref*1.3 {
			t.Errorf("%v Hz: a jump of %.0f at the join, the voice's own at most %.0f", f0, s, ref)
		}
	}
}

// Nothing is cut where a cut would be heard (noise, unvoiced sounds), from
// silence, or when the period does not fit what may be cut.
func TestAccelerateLeavesWhatIsNotVoiced(t *testing.T) {
	const frame = 160
	rng := rand.New(rand.NewSource(1))
	noise := func(n int) []int16 {
		s := make([]int16, n)
		for i := range s {
			s[i] = int16(rng.Intn(8000) - 4000)
		}
		return s
	}
	if _, cut := accelerate(noise(accelMaxPeriod), noise(frame), accelMaxPeriod); cut != 0 {
		t.Errorf("cut %d samples of noise", cut)
	}
	if _, cut := accelerate(make([]int16, accelMaxPeriod), make([]int16, frame), accelMaxPeriod); cut != 0 {
		t.Errorf("cut %d samples of silence", cut)
	}
	// 80 Hz: a 100-sample period; 60 may be cut.
	if out, cut := accelerate(voice(80, 0, accelMaxPeriod), voice(80, accelMaxPeriod, frame), 60); cut != 0 || len(out) != frame {
		t.Errorf("cut %d samples, at most 60 allowed and the period is 100", cut)
	}
	if _, cut := accelerate(nil, voice(150, 0, frame), accelMaxPeriod); cut != 0 {
		t.Errorf("cut %d samples with nothing played before", cut)
	}
}
