package device

import (
	"testing"
	"time"
)

func TestSystemVoiceCapturePreservesPCMAndTimeline(t *testing.T) {
	epoch := time.Unix(100, 0)
	out := make(chan AudioCaptureFrame, 4)
	c := systemVoiceCapture{sampleRate: 8000, frameSize: 160, epoch: epoch, out: out}
	position := 0
	// Native buffers can straddle codec frames or contain several of them.
	for _, size := range []int{0, 1, 79, 241, 0, 319} {
		input := make([]int16, size)
		for i := range input {
			input[i] = int16(position + i)
		}
		position += size
		c.write(input)
		clear(input) // the native unit reuses its callback buffer immediately
	}
	if len(out) != 4 {
		t.Fatalf("got %d frames, want 4", len(out))
	}
	for i := 0; i < 4; i++ {
		f := <-out
		if f.FirstSample != uint64(i*160) || !f.CapturedAt.Equal(epoch.Add(time.Duration(i)*20*time.Millisecond)) {
			t.Fatalf("frame %d: incorrect capture timeline: %+v", i, f)
		}
		if len(f.Samples) != 160 {
			t.Fatalf("frame %d has %d samples", i, len(f.Samples))
		}
		for j, v := range f.Samples {
			if v != int16(i*160+j) {
				t.Fatalf("frame %d sample %d: got %d, want %d", i, j, v, i*160+j)
			}
		}
	}
}

func TestSystemVoiceCaptureWaitsOnlyForCompleteFrame(t *testing.T) {
	out := make(chan AudioCaptureFrame, 2)
	c := systemVoiceCapture{sampleRate: 8000, frameSize: 160, out: out}
	c.write(nil)
	if !c.epoch.IsZero() {
		t.Fatal("empty callback started the capture clock")
	}
	before := time.Now().Add(-10 * time.Millisecond)
	c.write(make([]int16, 80))
	after := time.Now().Add(-10 * time.Millisecond)
	if len(out) != 0 {
		t.Fatal("woke the sender before a codec frame was available")
	}
	c.write(make([]int16, 79))
	if len(out) != 0 {
		t.Fatal("emitted an incomplete codec frame")
	}
	c.write([]int16{7})
	if len(out) != 1 {
		t.Fatal("did not emit immediately when the codec frame became complete")
	}
	f := <-out
	if f.CapturedAt.Before(before) || f.CapturedAt.After(after) {
		t.Fatalf("first sample time %v is outside [%v, %v]", f.CapturedAt, before, after)
	}
	if f.FirstSample != 0 || f.Samples[159] != 7 {
		t.Fatalf("bad first frame: %+v", f)
	}
}

func TestSystemVoiceCaptureDropsOldestWithoutCompressingTime(t *testing.T) {
	epoch := time.Unix(100, 0)
	out := make(chan AudioCaptureFrame, 2)
	c := systemVoiceCapture{sampleRate: 8000, frameSize: 160, epoch: epoch, out: out}
	c.write(make([]int16, 4*160+80))
	for _, first := range []uint64{320, 480} {
		select {
		case f := <-out:
			wantTime := epoch.Add(time.Duration(first) * time.Second / 8000)
			if f.FirstSample != first || !f.CapturedAt.Equal(wantTime) {
				t.Fatalf("dropped audio changed the timeline: first=%d time=%v", f.FirstSample, f.CapturedAt)
			}
		default:
			t.Fatal("missing newest frame after overflow")
		}
	}
	c.write(make([]int16, 80))
	select {
	case f := <-out:
		if f.FirstSample != 640 || !f.CapturedAt.Equal(epoch.Add(80*time.Millisecond)) {
			t.Fatalf("partial frame lost its position after overflow: %+v", f)
		}
	default:
		t.Fatal("missing completed partial frame")
	}
}
