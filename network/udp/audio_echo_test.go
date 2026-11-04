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
	return echoScenarioDelay(t, 240, playback, capture) // 30 ms echo path
}

// echoScenarioDelay is echoScenario with the echo arriving delay samples
// after the far frame was handed to the speaker.
func echoScenarioDelay(t *testing.T, delay int, playback func([]int16), capture func(mic, out []int16)) float64 {
	t.Helper()
	const fs = 160
	const frames = 500 // 10 s
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

// An 80 ms speaker callback can arrive with 40 ms of reference still queued
// while a 60 ms microphone callback is being scheduled. None is stale.
func TestEchoReferencePreservesSpeakerPeriodOverlap(t *testing.T) {
	a, err := newAudioEcho(160)
	if err != nil {
		t.Fatal(err)
	}
	a.setCaptureEnabled(true)
	for batch := 0; batch < 2; batch++ {
		pcm := make([]int16, 4*160)
		for i := range pcm {
			pcm[i] = int16(batch*4 + i/160 + 1)
		}
		a.playback(pcm)
		if batch == 0 {
			for want := int16(1); want <= 2; want++ {
				a.nextReference()
				if a.ref[0] != want {
					t.Fatalf("reference=%d, want %d", a.ref[0], want)
				}
			}
		}
	}
	for want := int16(3); want <= 8; want++ {
		a.nextReference()
		for _, sample := range a.ref {
			if sample != want {
				t.Fatalf("reference jumped to %d, want frame %d", sample, want)
			}
		}
	}
}

// With the v0.1.3 speaker buffer (80 ms x 4) the echo reaches the mic about
// 320 ms after the reference was rendered, beyond the 200 ms filter. The
// reference must be delayed by the device latency to be cancellable.
func TestEchoReferenceDelayCoversDeviceLatency(t *testing.T) {
	const deviceLatency = 4 * 4 * 160 // 320 ms
	const echoDelay = deviceLatency + 240
	run := func(refDelay int) float64 {
		a, err := newAudioEcho(160)
		if err != nil {
			t.Fatal(err)
		}
		a.setCaptureEnabled(true)
		a.setReferenceDelay(refDelay)
		return echoScenarioDelay(t, echoDelay, a.playback, func(m, o []int16) { _ = a.capture(m, o) })
	}
	delayed := run(deviceLatency - audioSampleRate*audioEchoDelayMarginMs/1000)
	undelayed := run(0)
	t.Logf("echo through a 320 ms device buffer: %.1f dB with reference delay, %.1f dB without", delayed, undelayed)
	if delayed < 15 {
		t.Fatalf("delayed reference suppresses only %.1f dB", delayed)
	}
}

func TestCaptureDropAdvancesEchoReference(t *testing.T) {
	a, err := newAudioEcho(160)
	if err != nil {
		t.Fatal(err)
	}
	a.setCaptureEnabled(true)
	for frame := 1; frame <= 8; frame++ {
		pcm := make([]int16, 160)
		for i := range pcm {
			pcm[i] = int16(frame)
		}
		a.playback(pcm)
	}
	a.skipCapture(3 * 160)
	if !a.nextReference() {
		t.Fatal("capture gap did not invalidate the learned echo path")
	}
	for _, v := range a.ref {
		if v != 4 {
			t.Fatalf("after 3 lost mic frames, reference is %d instead of 4", v)
		}
	}
	if a.nextReference() {
		t.Fatal("stable reference must not repeatedly reset the filter")
	}
}
