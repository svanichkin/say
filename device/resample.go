package device

import "math"

// rateConverter changes the sample rate of a mono stream: linear
// interpolation between the rates, with a low-pass FIR at the higher rate
// (before the interpolation when going down, against aliasing; after it when
// going up, against images). It keeps its state between calls, so a stream
// can be converted in blocks of any size. Used where Say talks to the sound
// device itself (Plan 9), without miniaudio's converter.
type rateConverter struct {
	inRate, outRate int
	// phase is the next output instant within the current input interval,
	// in 1/outRate of the interval; outputs are inRate apart.
	phase int
	prev  float32
	fir   *firFilter // nil when the rates are equal
}

func newRateConverter(inRate, outRate int) *rateConverter {
	c := &rateConverter{inRate: inRate, outRate: outRate}
	if inRate != outRate {
		high, low := max(inRate, outRate), min(inRate, outRate)
		// Pass the voice band up to 85% of the low rate's Nyquist frequency.
		c.fir = newLowPass(0.85*float64(low)/2/float64(high), 127)
	}
	return c
}

// convert appends the converted samples of in to out and returns out.
func (c *rateConverter) convert(out []float32, in []float32) []float32 {
	if c.fir == nil {
		return append(out, in...)
	}
	down := c.inRate > c.outRate
	for _, x := range in {
		if down {
			x = c.fir.step(x)
		}
		for ; c.phase < c.outRate; c.phase += c.inRate {
			y := c.prev + (x-c.prev)*float32(c.phase)/float32(c.outRate)
			if !down {
				y = c.fir.step(y)
			}
			out = append(out, y)
		}
		c.phase -= c.outRate
		c.prev = x
	}
	return out
}

// firFilter is a FIR filter over a doubled ring buffer, so every output is
// one contiguous dot product.
type firFilter struct {
	taps []float32
	hist []float32 // 2*len(taps): each sample is stored twice
	pos  int
}

// newLowPass is a Blackman-windowed sinc with the cutoff given as a fraction
// of the sample rate, normalised to unity gain at DC.
func newLowPass(cutoff float64, n int) *firFilter {
	taps := make([]float32, n)
	m := float64(n - 1)
	var sum float64
	h := make([]float64, n)
	for i := range h {
		x := float64(i) - m/2
		sinc := 2 * cutoff
		if x != 0 {
			sinc = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/m) + 0.08*math.Cos(4*math.Pi*float64(i)/m)
		h[i] = sinc * w
		sum += h[i]
	}
	for i := range h {
		taps[i] = float32(h[i] / sum)
	}
	return &firFilter{taps: taps, hist: make([]float32, 2*n)}
}

func (f *firFilter) step(x float32) float32 {
	n := len(f.taps)
	f.pos--
	if f.pos < 0 {
		f.pos = n - 1
	}
	f.hist[f.pos] = x
	f.hist[f.pos+n] = x
	var y float32
	for i, t := range f.taps {
		y += t * f.hist[f.pos+i]
	}
	return y
}

func clampInt16(v float32) int16 {
	switch {
	case v >= math.MaxInt16:
		return math.MaxInt16
	case v <= math.MinInt16:
		return math.MinInt16
	}
	return int16(math.Round(float64(v)))
}
