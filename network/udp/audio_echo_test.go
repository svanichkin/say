package udp

import (
	"math"
	"math/rand"
	"testing"

	"github.com/danieldonoghue/govoip-dsp/echo"
)

// echoScenario plays far-end noise through a simulated room (echo delayed by
// 30 ms) while the speaker and microphone callbacks arrive in irregular
// bursts, as two independent devices do. It returns the echo suppression in
// dB over the last two seconds.
func echoScenario(t *testing.T, playback func([]int16), capture func(mic, out []int16)) float64 {
	t.Helper()
	const fs = 160
	const frames = 500 // 10 s
	const delay = 240  // 30 ms echo path
	rng := rand.New(rand.NewSource(1))
	far := make([]int16, (frames+2)*fs)
	for i := range far {
		far[i] = int16(rng.NormFloat64() * 3000)
	}
	mic := func(k int) []int16 {
		m := make([]int16, fs)
		for i := range m {
			if j := k*fs + i - delay; j >= 0 {
				m[i] = far[j] / 2
			}
		}
		return m
	}
	out := make([]int16, fs)
	var inE, outE float64
	played, captured := 0, 0
	for captured < frames {
		// Bursty interleaving: up to 3 callbacks of one device before the other.
		for n := rng.Intn(3); n > 0 && played < frames && played-captured < 3; n-- {
			playback(far[played*fs : (played+1)*fs])
			played++
		}
		for n := rng.Intn(3); n > 0 && captured < played; n-- {
			m := mic(captured)
			capture(m, out)
			if captured >= frames-100 {
				for i := range m {
					inE += float64(m[i]) * float64(m[i])
					outE += float64(out[i]) * float64(out[i])
				}
			}
			captured++
		}
	}
	return 10 * math.Log10(inE/math.Max(outE, 1))
}

func TestEchoReferenceSurvivesCallbackJitter(t *testing.T) {
	a, err := newAudioEcho(160)
	if err != nil {
		t.Fatal(err)
	}
	a.setCaptureEnabled(true)
	buffered := echoScenario(t, a.playback, func(m, o []int16) { _ = a.capture(m, o) })

	// The previous wiring fed the canceller straight from both callbacks.
	c, err := echo.New(echo.Config{FrameSize: 160, FilterLength: audioSampleRate * audioEchoTailMs / 1000, SampleRate: audioSampleRate})
	if err != nil {
		t.Fatal(err)
	}
	direct := echoScenario(t, func(f []int16) { _ = c.Playback(f) }, func(m, o []int16) { _ = c.Capture(m, o) })

	t.Logf("echo suppression with jittery callbacks: buffered %.1f dB, direct %.1f dB", buffered, direct)
	if buffered < 15 {
		t.Fatalf("buffered reference suppresses only %.1f dB of echo", buffered)
	}
}
