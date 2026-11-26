// Package tones synthesises Say's own signals, in the style of the music of
// XM keygens: pulse waves, fast arpeggios, a triangle bass, an echo. They
// are made here, sample by sample, from nothing recorded.
//
//   - Ring: what both sides hear while a call rings (the one called, and
//     the caller waiting): a short tune, a lead over chords and a bass,
//     its answer leading back into it, round and round without a pause.
//   - Fail: played once when a call fails (the line busy, the call declined
//     or unanswered): a short sour buzz.
package tones

import "math"

// Rate is the sample rate the tones are made at.
const Rate = 44100

// Ring is one period of the ring (mono, Rate), to loop without a pause:
// the tune, then its answer leading back into it, a flowing round.
func Ring() []int16 {
	const step = 60.0 / 150 / 4 // sixteenths at 150 a minute
	// A note a step, 0 a rest, -1 the note before held.
	melody := []float64{
		76, 0, 79, 81, 0, 79, 76, 74, 76, 0, 71, 74, 76, -1, -1, -1, // the tune
		79, 0, 81, 83, 0, 81, 79, 76, 74, 0, 71, 74, 76, -1, 71, 74, // its answer, back to the start
	}
	chords := [][3]float64{{64, 67, 71}, {62, 66, 69}, {60, 64, 67}, {62, 66, 69}, // Em D C D
		{60, 64, 67}, {62, 66, 69}, {64, 67, 71}, {62, 66, 69}} // C D Em D
	bass := []float64{40, 38, 36, 38, 36, 38, 40, 38}
	buf := make([]float64, int(float64(len(melody))*step*Rate))
	for i, n := range melody {
		if n <= 0 {
			continue
		}
		held := 1
		for i+held < len(melody) && melody[i+held] < 0 {
			held++
		}
		note(buf, float64(i)*step, (float64(held)-0.1)*step, n, 0.5, 0.5, 5)
	}
	for b, c := range chords { // a chord a beat, as an arpeggio
		arp(buf, float64(b)*4*step, 4*step*0.95, c, 0.25, 0.18)
		tri(buf, float64(b)*4*step, 4*step*0.9, bass[b], 0.35)
	}
	echo(buf, 0.15, 0.3) // round the end: the loop has no seam
	return pcm(buf, 0.55)
}

// Fail is the fail sound (mono, Rate), played once: a low E and the F above
// it at once, rattling at an XM tick, over a triangle an octave down.
func Fail() []int16 {
	buf := make([]float64, int(0.7*Rate))
	rattle := func(t float64) float64 { return 40 + float64(int(t*50)%2) }
	voice(buf, 0.35, 0.5, func(p, _ float64) float64 { return pulse(p, 0.5) }, rattle)
	voice(buf, 0.35, 0.4, triangle, func(float64) float64 { return 28 })
	return pcm(buf, 0.55)
}

func hz(midi float64) float64 { return 440 * math.Pow(2, (midi-69)/12) }

// pulse is a pulse wave's value at phase p (cycles), duty d.
func pulse(p, d float64) float64 {
	if p-math.Floor(p) < d {
		return 1
	}
	return -1
}

// env is a quick attack and a decay to nothing over dur.
func env(t, dur float64) float64 {
	const attack = 0.004
	if t < attack {
		return t / attack
	}
	return math.Pow(1-(t-attack)/(dur-attack), 1.5)
}

// note adds a pulse note (duty d, level a) with vibrato depth vib (cents).
func note(buf []float64, s, dur, midi, d, a, vib float64) {
	phase := 0.0
	for i := 0; i < int(dur*Rate); i++ {
		t := float64(i) / Rate
		n := midi + vib/100*math.Sin(2*math.Pi*6*t)
		phase += hz(n) / Rate
		add(buf, s, i, a*env(t, dur)*pulse(phase, d))
	}
}

// arp adds a chord as an arpeggio, a step 1/50 s (an XM tick), duty d.
func arp(buf []float64, s, dur float64, chord [3]float64, d, a float64) {
	phase := 0.0
	for i := 0; i < int(dur*Rate); i++ {
		t := float64(i) / Rate
		phase += hz(chord[int(t*50)%3]) / Rate
		add(buf, s, i, a*env(t, dur)*pulse(phase, d))
	}
}

// tri adds a triangle note, the bass.
func tri(buf []float64, s, dur, midi, a float64) {
	f := hz(midi)
	for i := 0; i < int(dur*Rate); i++ {
		t := float64(i) / Rate
		p := t*f - math.Floor(t*f)
		add(buf, s, i, a*env(t, dur)*(4*math.Abs(p-0.5)-1))
	}
}

// voice adds a plucked sound (env) from the start, dur long, at level a:
// its wave of phase and time, its pitch (midi) of time.
func voice(buf []float64, dur, a float64, wave func(p, t float64) float64, pitch func(t float64) float64) {
	phase := 0.0
	for i := 0; i < int(dur*Rate); i++ {
		t := float64(i) / Rate
		phase += hz(pitch(t)) / Rate
		add(buf, 0, i, a*env(t, dur)*wave(phase, t))
	}
}

func triangle(p, _ float64) float64 {
	q := p - math.Floor(p)
	return 4*math.Abs(q-0.5) - 1
}

func add(buf []float64, s float64, i int, v float64) {
	if j := int(s*Rate) + i; j >= 0 && j < len(buf) {
		buf[j] += v
	}
}

// echo feeds the buffer back into itself delay seconds later, round the
// period's end (the tone loops).
func echo(buf []float64, delay, feedback float64) {
	d := int(delay * Rate)
	out := append([]float64(nil), buf...)
	for pass := 0; pass < 2; pass++ { // twice round, the tail wraps
		for i := range out {
			out[i] = buf[i] + feedback*out[(i-d+len(out))%len(out)]
		}
	}
	copy(buf, out)
}

// pcm scales the buffer to peak at level of full scale.
func pcm(buf []float64, level float64) []int16 {
	peak := 0.0
	for _, v := range buf {
		peak = math.Max(peak, math.Abs(v))
	}
	out := make([]int16, len(buf))
	if peak == 0 {
		return out
	}
	k := level * 32767 / peak
	for i, v := range buf {
		out[i] = int16(v * k)
	}
	return out
}
