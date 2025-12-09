package tcp

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"github.com/svanichkin/say/logs"
	"sync"
	"time"
)

// tcpKeepalive tracks the last time we saw traffic on the control connection and
// enforces an application-level timeout on missing tick responses.
type tcpKeepalive struct {
	mu       sync.Mutex
	lastSeen time.Time
	timeout  time.Duration
}

// touch records that traffic has been seen on the connection (e.g. a frame was read).
func (ka *tcpKeepalive) touch() {
	if ka == nil {
		return
	}
	ka.mu.Lock()
	ka.lastSeen = time.Now()
	ka.mu.Unlock()
}

// expired reports whether the interval since lastSeen exceeds the configured timeout.
func (ka *tcpKeepalive) expired(now time.Time) bool {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	return now.Sub(ka.lastSeen) > ka.timeout
}

// startTCPKeepalive starts application-level keepalive logic for the control connection.
// When send is true it periodically sends tick frames and expects tick responses within
// the timeout; if ticks stop arriving, the connection is closed. The returned tcpKeepalive
// can be updated via touch() when frames are received, and the cancel function stops
// the keepalive goroutines and waits for them to exit.
func startTCPKeepalive(ctx context.Context, conn net.Conn, interval, timeout time.Duration, send bool) (*tcpKeepalive, func()) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if timeout <= interval {
		timeout = interval * 3
	}

	ka := &tcpKeepalive{lastSeen: time.Now(), timeout: timeout}
	keepCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	if !send {
		return ka, func() {
			cancel()
		}
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-keepCtx.Done():
				return
			case <-ticker.C:
				logs.LogV("[keepalive] tick → %T", conn)
				buf := make([]byte, 8)
				now := time.Now().UTC()
				binary.BigEndian.PutUint64(buf, uint64(now.UnixNano()))
				if err := writeFrameSafe(conn, frameTick, buf); err != nil {
					log.Printf("[keepalive] tick send failed (write): %v", err)
					_ = conn.Close()
					return
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval / 2)
		defer ticker.Stop()
		for {
			select {
			case <-keepCtx.Done():
				return
			case <-ticker.C:
				if ka.expired(time.Now()) {
					log.Printf("[keepalive] timeout waiting tick (conn=%T)", conn)
					_ = conn.Close()
					return
				}
			}
		}
	}()

	return ka, func() {
		cancel()
		wg.Wait()
	}
}

const tickPayloadLen = 8

// decodeTickLatency interprets a frameTick payload containing a UTC timestamp (Unix nano).
// It returns the one-way latency as observed locally.
func decodeTickLatency(payload []byte) (time.Duration, bool) {
	if len(payload) != tickPayloadLen {
		return 0, false
	}
	ns := int64(binary.BigEndian.Uint64(payload))
	if ns <= 0 {
		return 0, false
	}
	sent := time.Unix(0, ns).UTC()
	now := time.Now().UTC()
	if now.Before(sent) {
		return 0, false
	}
	return now.Sub(sent), true
}
