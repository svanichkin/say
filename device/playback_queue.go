package device

import (
	"sync/atomic"
	"time"
)

// PlaybackQueue carries decoded PCM from the network receiver to the speaker.
// It tracks how many samples are waiting to be played (queued frames plus the
// unplayed rest of the frame the speaker is currently on), so the receiver can
// bound playback latency in time. A frame count alone is not enough: a single
// concealment frame can span hundreds of milliseconds.
type PlaybackQueue struct {
	ch      chan PlaybackFrame
	samples atomic.Int64
	// gap is set when queued audio was discarded, so the next frame the
	// speaker takes does not continue the one it is playing.
	gap atomic.Bool
}

// NewPlaybackQueue creates a queue holding at most capacity frames.
func NewPlaybackQueue(capacity int) *PlaybackQueue {
	if capacity < 1 {
		capacity = 1
	}
	return &PlaybackQueue{ch: make(chan PlaybackFrame, capacity)}
}

// Push enqueues a frame, dropping the oldest queued frames if the queue is full.
func (q *PlaybackQueue) Push(frame PlaybackFrame) {
	if q == nil || len(frame.Samples) == 0 {
		return
	}
	n := int64(len(frame.Samples))
	// Count before sending so the speaker can never consume more than was added.
	q.samples.Add(n)
	for {
		select {
		case q.ch <- frame:
			return
		default:
			if !q.DropOldest() {
				q.samples.Add(-n)
				return
			}
		}
	}
}

// DropOldest discards the oldest queued frame. It returns false if the queue is empty.
func (q *PlaybackQueue) DropOldest() bool {
	if q == nil {
		return false
	}
	select {
	case frame := <-q.ch:
		q.samples.Add(-int64(len(frame.Samples)))
		q.gap.Store(true)
		queueDrops.Add(1)                                                                                             // DEBUG-TEMP
		tracePlay(PlayEvent{Kind: PlayDropped, Index: frame.MediaIndex, At: time.Now(), Samples: len(frame.Samples)}) // DEBUG-TEMP
		return true
	default:
		return false
	}
}

// Clear discards every queued frame.
func (q *PlaybackQueue) Clear() {
	for q.DropOldest() {
	}
}

// QueuedSamples returns how many samples are waiting to be played.
func (q *PlaybackQueue) QueuedSamples() int {
	if q == nil {
		return 0
	}
	return int(q.samples.Load())
}

// TrimLatency drops the oldest frames once more than maxSamples are waiting,
// until at most targetSamples remain. The newest queued frame is always kept,
// so trimming skips stale audio but never silences fresh audio. It returns the
// number of frames dropped.
func (q *PlaybackQueue) TrimLatency(maxSamples, targetSamples int) int {
	if q == nil || q.QueuedSamples() <= maxSamples {
		return 0
	}
	dropped := 0
	for len(q.ch) > 1 && q.QueuedSamples() > targetSamples && q.DropOldest() {
		dropped++
	}
	return dropped
}

// pop takes the next frame for the speaker without changing the sample count;
// the speaker reports played samples through consumed. discontinuous is true
// when frames before it were discarded.
func (q *PlaybackQueue) pop() (frame PlaybackFrame, discontinuous, ok bool) {
	select {
	case frame, ok = <-q.ch:
		return frame, ok && q.gap.Swap(false), ok
	default:
		return PlaybackFrame{}, false, false
	}
}

func (q *PlaybackQueue) consumed(n int) {
	if n > 0 {
		q.samples.Add(-int64(n))
	}
}

// TakeFrame removes and accounts for the next whole frame, for consumers
// that play complete frames at once (offline rendering, tests).
func (q *PlaybackQueue) TakeFrame() (PlaybackFrame, bool) {
	frame, _, ok := q.pop()
	if ok {
		q.consumed(len(frame.Samples))
	}
	return frame, ok
}
