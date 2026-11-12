//go:build !plan9

package device

import (
	"testing"
	"time"
)

func rampFrame(index uint16, n int, from int16) PlaybackFrame {
	s := make([]int16, n)
	for i := range s {
		s[i] = from + int16(i)
	}
	return PlaybackFrame{Samples: s, MediaIndex: index, MediaSamples: n}
}

// What the playout queued reaches the unit sample for sample; while it
// sits in the ring it still counts as queued, and the media clock follows
// what the unit took, not what was written.
func TestRingRendererPlaysTheQueue(t *testing.T) {
	vio, _ := newNativeRing(8000, 1)
	defer vio.free()
	q := NewPlaybackQueue(16)
	sp := &Speaker{}
	r := &ringRenderer{speaker: sp, in: q, ring: vio, samplesPerMediaTick: 8, ahead: 2400}

	q.Push(rampFrame(100, 160, 1000))
	q.Push(rampFrame(120, 160, 2000))
	r.step()
	if st := vio.State(); st.Written != 320 || q.QueuedSamples() != 320 {
		t.Fatalf("written %d, queued %d; want 320 in the ring, all still counted as queued", st.Written, q.QueuedSamples())
	}
	if sp.playbackSet.Load() {
		t.Fatal("media clock set before the unit played anything")
	}

	out := make([]int16, 80)
	vio.devicePlay(out) // half of the first frame
	if out[0] != 1000 || out[79] != 1079 {
		t.Fatalf("played %d..%d, want 1000..1079", out[0], out[79])
	}
	r.step()
	if q.QueuedSamples() != 240 {
		t.Fatalf("queued %d after 80 played, want 240", q.QueuedSamples())
	}
	if got := sp.playbackTick.Load(); got != 110 { // 80 of 160 samples: 10 ticks of 8 into the frame at 100
		t.Fatalf("media clock %d, want 110", got)
	}

	big := make([]int16, 240)
	vio.devicePlay(big) // the rest of both frames
	if big[80] != 2000 || big[239] != 2159 {
		t.Fatalf("second frame played %d..%d, want 2000..2159", big[80], big[239])
	}
	r.step()
	if q.QueuedSamples() != 0 || sp.playbackTick.Load() != 140 {
		t.Fatalf("queued %d, clock %d after all played; want 0, 140", q.QueuedSamples(), sp.playbackTick.Load())
	}
}

// Running dry, the unit ramps the last sample down instead of stepping to
// silence, and ramps back in when audio returns.
func TestRingRunningDryRamps(t *testing.T) {
	vio, _ := newNativeRing(8000, 1)
	defer vio.free()
	loud := make([]int16, 80)
	for i := range loud {
		loud[i] = 10000
	}
	vio.Write(loud)
	out := make([]int16, 160)
	vio.devicePlay(out)
	if out[79] != 10000 || out[80] >= 10000 || out[80] <= 0 || out[80+declickSamples] != 0 || out[159] != 0 {
		t.Fatalf("no ramp down: %d %d %d %d", out[79], out[80], out[80+declickSamples], out[159])
	}
	if st := vio.State(); st.DryCallbacks != 1 || st.DrySamples != 80 {
		t.Fatalf("dry %d callbacks, %d samples; want 1, 80", st.DryCallbacks, st.DrySamples)
	}
	vio.Write(loud)
	vio.devicePlay(out[:80])
	if out[0] >= 10000/2 || out[79] != 10000 {
		t.Fatalf("no ramp in: %d .. %d", out[0], out[79])
	}
}

// Microphone blocks keep the time they were captured, however late they
// are read.
func TestRingMicKeepsCaptureTime(t *testing.T) {
	vio, _ := newNativeRing(8000, 1)
	defer vio.free()
	block := make([]int16, 80)
	block[0] = 7
	vio.deviceCapture(block, 400*time.Millisecond)
	buf := make([]int16, 1024)
	n, at := vio.ReadMic(buf)
	if n != 80 || buf[0] != 7 {
		t.Fatalf("read %d samples starting %d", n, buf[0])
	}
	if d := time.Since(at); d < 390*time.Millisecond || d > 450*time.Millisecond {
		t.Fatalf("block stamped %v ago, want ~400 ms", d)
	}
	if n, _ := vio.ReadMic(buf); n != 0 {
		t.Fatalf("a second block of %d samples out of one", n)
	}
	// A full ring drops what does not fit and says so.
	for i := 0; i < 200; i++ {
		vio.deviceCapture(block, 0)
	}
	if st := vio.State(); st.MicDropped != 72*80 {
		t.Fatalf("%d samples dropped, want %d", st.MicDropped, 72*80)
	}
}

// The echo canceller's reference is what the device played, silence
// included, in the same count of samples.
func TestRingRendererReference(t *testing.T) {
	vio, _ := newNativeRing(8000, 1)
	defer vio.free()
	q := NewPlaybackQueue(16)
	var ref []int16
	r := &ringRenderer{speaker: &Speaker{}, in: q, ring: vio, ahead: 2400, onRender: func(s []int16) { ref = append(ref, s...) }}
	q.Push(rampFrame(0, 160, 100))
	r.step()
	vio.devicePlay(make([]int16, 80))
	r.step()
	vio.devicePlay(make([]int16, 200)) // 80 more, then 120 of silence
	r.step()
	if len(ref) != 280 {
		t.Fatalf("reference of %d samples, the device played 280", len(ref))
	}
	if ref[0] != 100 || ref[79] != 179 {
		t.Fatalf("reference starts %d..%d, want 100..179", ref[0], ref[79])
	}
}

// Closing returns everything written but not played to the queue's count.
func TestRingRendererRelease(t *testing.T) {
	vio, _ := newNativeRing(8000, 1)
	q := NewPlaybackQueue(16)
	r := &ringRenderer{speaker: &Speaker{}, in: q, ring: vio, ahead: 100}
	q.Push(rampFrame(0, 160, 0))
	r.step() // 100 written, 60 pending
	vio.devicePlay(make([]int16, 30))
	r.final = vio.State()
	r.release()
	vio.free()
	if q.QueuedSamples() != 0 {
		t.Fatalf("%d samples still counted as queued", q.QueuedSamples())
	}
}
