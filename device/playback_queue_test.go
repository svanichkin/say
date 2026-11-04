package device

import "testing"

func TestPlaybackQueueBoundsLatencyBySamples(t *testing.T) {
	const frame = 160 // 20 ms at 8 kHz
	q := NewPlaybackQueue(32)
	maxSamples, target := 15*frame, 6*frame
	// 32 concealment frames of 640 ms each would be 20 s of audio.
	for i := 0; i < 32; i++ {
		q.Push(PlaybackFrame{Samples: make([]int16, 32*frame), MediaIndex: uint16(i)})
		q.TrimLatency(maxSamples, target)
	}
	if got := q.QueuedSamples(); got != 32*frame {
		t.Fatalf("queued %d samples (%d ms), want exactly the newest concealment frame", got, got/8)
	}
	if f, ok := q.pop(); !ok || f.MediaIndex != 31 {
		t.Fatalf("kept frame %d (ok=%v), want the newest frame 31", f.MediaIndex, ok)
	} else {
		q.consumed(len(f.Samples))
	}
	// Regular 20 ms frames must settle at or below the trim limit.
	q.Clear()
	for i := 0; i < 100; i++ {
		q.Push(PlaybackFrame{Samples: make([]int16, frame)})
		q.TrimLatency(maxSamples, target)
	}
	if got := q.QueuedSamples(); got > maxSamples {
		t.Fatalf("queued %d samples, want <= %d", got, maxSamples)
	}
	// The speaker's accounting returns the counter to zero once all is played.
	for {
		f, ok := q.pop()
		if !ok {
			break
		}
		q.consumed(len(f.Samples))
	}
	if got := q.QueuedSamples(); got != 0 {
		t.Fatalf("queued %d samples after draining, want 0", got)
	}
}
