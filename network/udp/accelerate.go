package udp

import "math"

// Speech played faster to catch up, as WebRTC's NetEq does it (Accelerate):
// voiced speech repeats its pitch period, so a whole period can be cut out,
// with a crossfade over the join. The speech then goes faster at its own
// pitch, where resampling (resizePCM) would raise the voice with the tempo.
const (
	// accelMinPeriod and accelMaxPeriod: the pitch periods looked for,
	// 2.5 ms (400 Hz) to 15 ms (67 Hz), voices high and low.
	accelMinPeriod = audioSampleRate * 25 / 10000
	accelMaxPeriod = audioSampleRate * 15 / 1000
	// accelMinOverlap: the shortest crossfade over the join (2.5 ms).
	accelMinOverlap = audioSampleRate * 25 / 10000
	// accelMinCorrelation: how alike a frame and the same frame a period
	// earlier must be for a period to be cut (NetEq's threshold for voiced
	// speech). Below it (unvoiced sounds, noise) a cut would be heard.
	accelMinCorrelation = 0.9
	// audioAccelPermille: how much faster speech plays at most while
	// catching up (250: 25%, a period cut every few frames).
	audioAccelPermille = 250
)

// accelerate cuts one pitch period, of at most maxCut samples, out of frame.
// hist is what was played just before frame (oldest first): the period is
// looked for across the two, so that a low voice, whose period is most of a
// frame, is found too. It returns the shortened frame and the samples cut,
// or frame itself and 0 when frame is not periodic enough. The first sample
// stays, so the frame still joins the one before it.
func accelerate(hist, frame []int16, maxCut int) ([]int16, int) {
	n := len(frame)
	hi := min(accelMaxPeriod, maxCut, n-accelMinOverlap, len(hist))
	if hi < accelMinPeriod {
		return frame, 0
	}
	// z: the last hi samples played, then the frame (at z[hi:]).
	z := make([]float64, hi+n)
	for i, v := range hist[len(hist)-hi:] {
		z[i] = float64(v)
	}
	for i, v := range frame {
		z[hi+i] = float64(v)
	}
	var energy float64
	for _, v := range z[hi:] {
		energy += v * v
	}
	if energy == 0 {
		return frame, 0
	}
	// The period: the lag at which the frame best matches itself that much
	// earlier (normalised cross-correlation).
	best, bestR := 0, accelMinCorrelation
	for p := accelMinPeriod; p <= hi; p++ {
		var c, e float64
		for i := hi; i < len(z); i++ {
			c += z[i] * z[i-p]
			e += z[i-p] * z[i-p]
		}
		if e == 0 {
			continue
		}
		if r := c / math.Sqrt(energy*e); r >= bestR {
			best, bestR = p, r // ties: the longer period, more cut at once
		}
	}
	if best == 0 {
		return frame, 0
	}
	// Cut frame[0:best]: frame[i] fades into frame[i+best], which repeats it
	// a period on, over the overlap; the rest follows.
	overlap := min(best, n-best)
	out := make([]int16, n-best)
	for i := range overlap {
		w := float64(i) / float64(overlap)
		out[i] = int16(math.Round(float64(frame[i])*(1-w) + float64(frame[i+best])*w))
	}
	copy(out[overlap:], frame[best+overlap:])
	return out, best
}
