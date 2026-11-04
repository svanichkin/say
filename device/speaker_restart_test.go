package device

import (
	"testing"
	"time"
)

// A speaker closed mid-frame (the -audio command replaces it during a call)
// must not leave the unplayed rest of that frame counted in the queue: the
// next speaker's playout would see audio that is not there.
func TestSpeakerCloseReturnsUnplayedSamples(t *testing.T) {
	const rate, frame = 8000, 160
	q := NewPlaybackQueue(64)
	stop := make(chan struct{})
	defer close(stop)
	sp, err := StartSpeaker(rate, 1, frame*3+37, 2, 32, q, stop, nil, nil)
	if err != nil {
		t.Skipf("no playback device: %v", err)
	}
	for i := 0; i < 50; i++ {
		q.Push(PlaybackFrame{Samples: make([]int16, frame), MediaIndex: uint16(i * 5)})
	}
	deadline := time.Now().Add(2 * time.Second)
	for q.QueuedSamples() == 50*frame && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	sp.Close()
	if got, want := q.QueuedSamples(), len(q.ch)*frame; got != want {
		t.Fatalf("after Close the queue counts %d samples, but %d are queued", got, want)
	}
}
