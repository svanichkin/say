package ui

import (
	"testing"
	"time"
)

func TestPathQueue(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	var m pathQueueMeter
	t0 := time.Unix(1000, 0)
	if q := m.queue(t0); q != 0 {
		t.Fatalf("queue with no round trips: %v", q)
	}
	for i := 0; i < 10; i++ {
		m.note(ms(150+10*(i%3)), t0.Add(time.Duration(i)*2*time.Second))
	}
	if q := m.queue(t0.Add(20 * time.Second)); q > ms(30) {
		t.Fatalf("a steady path reads as a %v queue", q)
	}
	// The pongs are held up: the oldest ping out for 3 s is a 3 s round trip.
	m.out = t0.Add(20 * time.Second)
	if q := m.queue(t0.Add(23 * time.Second)); q < ms(2800) {
		t.Fatalf("a ping out for 3 s reads as a %v queue", q)
	}
	m.out = time.Time{}
	m.note(ms(2150), t0.Add(24*time.Second))
	if q := m.queue(t0.Add(24 * time.Second)); q != ms(2000) {
		t.Fatalf("queue %v, want 2s over the 150 ms base", q)
	}
	// The base forgets round trips older than the window.
	for i := 0; i < 80; i++ {
		m.note(ms(400), t0.Add(30*time.Second+time.Duration(i)*2*time.Second))
	}
	if q := m.queue(t0.Add(190 * time.Second)); q != 0 {
		t.Fatalf("after the window a 400 ms path reads as a %v queue", q)
	}
}
