package udp

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

type deadlineTestConn struct {
	net.PacketConn
	mu                sync.Mutex
	deadline          time.Time
	entered           chan struct{}
	active, maxActive int
}

func (c *deadlineTestConn) SetWriteDeadline(d time.Time) error {
	c.mu.Lock()
	c.deadline = d
	c.mu.Unlock()
	return nil
}
func (c *deadlineTestConn) WriteTo(_ []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	deadline := c.deadline
	c.active++
	c.maxActive = max(c.maxActive, c.active)
	c.mu.Unlock()
	c.entered <- struct{}{}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return 0, os.ErrDeadlineExceeded
}

func TestMediaWritesHaveBoundedDeadlinesAndStopWaiters(t *testing.T) {
	raw := &deadlineTestConn{entered: make(chan struct{}, 2)}
	stop := make(chan struct{})
	conn := newMediaPacketConn(raw, stop)
	done := make(chan error, 2)
	go func() { _, err := conn.WriteTo([]byte{1}, testPeer()); done <- err }()
	select {
	case <-raw.entered:
	case <-time.After(time.Second):
		t.Fatal("write not started")
	}
	go func() { _, err := conn.WriteTo([]byte{2}, testPeer()); done <- err }()
	close(stop)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("blocked write succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("write or waiter failed to stop")
		}
	}
	raw.mu.Lock()
	defer raw.mu.Unlock()
	if raw.maxActive != 1 {
		t.Fatalf("shared socket deadlines raced across %d writes", raw.maxActive)
	}
	if len(raw.entered) != 0 {
		t.Fatal("waiting writer entered the transport after stop")
	}
}
