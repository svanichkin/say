package ui

import (
	"sync"
	"time"
)

// The queue on the way to the peer.
//
// Where the path carries less than is sent, what is sent waits in a queue
// (an uplink's buffer, a direct link's QUIC stream) and arrives seconds
// late without being lost: loss reports stay clean while the call falls
// behind. The keepalive round trip shows it: the queue is the round trip now
// less the shortest of the last two minutes. While pongs are held up behind
// the queue, the oldest unanswered ping counts as that long a round trip
// already.

const (
	pathBaseWindow = 2 * time.Minute
	pathBaseBucket = 10 * time.Second
)

type pathQueueMeter struct {
	mu      sync.Mutex
	last    time.Duration // the latest round trip
	out     time.Time     // when the oldest unanswered ping was sent (zero: none)
	buckets []pathBucket  // the shortest round trip per pathBaseBucket
}

type pathBucket struct {
	start time.Time
	min   time.Duration
}

var pathQueue pathQueueMeter

// ResetPathQueue forgets the last call's round trips.
func ResetPathQueue() {
	pathQueue.mu.Lock()
	pathQueue.last, pathQueue.out, pathQueue.buckets = 0, time.Time{}, nil
	pathQueue.mu.Unlock()
}

// NotePathRTT takes a keepalive round trip.
func NotePathRTT(rtt time.Duration) { pathQueue.note(rtt, time.Now()) }

// NotePingsOut takes when the oldest unanswered keepalive ping was sent
// (zero: none is out).
func NotePingsOut(oldest time.Time) {
	pathQueue.mu.Lock()
	pathQueue.out = oldest
	pathQueue.mu.Unlock()
}

// PathQueue is how much longer than its usual the round trip to the peer is
// now (0 while unknown).
func PathQueue() time.Duration { return pathQueue.queue(time.Now()) }

func (m *pathQueueMeter) note(rtt time.Duration, now time.Time) {
	if rtt <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = rtt
	n := len(m.buckets)
	if n == 0 || now.Sub(m.buckets[n-1].start) >= pathBaseBucket {
		m.buckets = append(m.buckets, pathBucket{start: now, min: rtt})
	} else if rtt < m.buckets[n-1].min {
		m.buckets[n-1].min = rtt
	}
	for len(m.buckets) > 1 && now.Sub(m.buckets[0].start) > pathBaseWindow {
		m.buckets = m.buckets[1:]
	}
}

func (m *pathQueueMeter) queue(now time.Time) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buckets) == 0 {
		return 0
	}
	base := m.buckets[0].min
	for _, b := range m.buckets[1:] {
		if b.min < base {
			base = b.min
		}
	}
	cur := m.last
	if !m.out.IsZero() && now.Sub(m.out) > cur {
		cur = now.Sub(m.out)
	}
	if cur < base {
		return 0
	}
	return cur - base
}
