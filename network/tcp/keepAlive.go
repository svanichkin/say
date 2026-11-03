package tcp

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"net"
	"sync"
	"time"

	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

const (
	pingPayloadLen = 8
	pongPayloadLen = 9
)

// tcpKeepalive tracks outstanding ping timestamps using the local monotonic clock.
// A pong echoes the ping ID, so RTT does not depend on the peers' wall clocks.
type tcpKeepalive struct {
	mu       sync.Mutex
	lastPong time.Time
	timeout  time.Duration
	nextID   uint64
	pending  map[uint64]time.Time
}

func (ka *tcpKeepalive) newPing() []byte {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	ka.nextID++
	id := ka.nextID
	ka.pending[id] = time.Now()
	for pendingID, sentAt := range ka.pending {
		if time.Since(sentAt) > ka.timeout {
			delete(ka.pending, pendingID)
		}
	}
	payload := make([]byte, pingPayloadLen)
	binary.BigEndian.PutUint64(payload, id)
	return payload
}

func (ka *tcpKeepalive) pongPayload(ping []byte) []byte {
	if len(ping) != pingPayloadLen {
		return nil
	}
	payload := make([]byte, pongPayloadLen)
	copy(payload, ping)
	loss, updatedAt := ui.RxAudioLossSnapshot()
	if updatedAt.IsZero() || time.Since(updatedAt) > 3*time.Second {
		loss = 0
	}
	pct := math.Round(loss * 100)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	payload[pingPayloadLen] = byte(pct)
	return payload
}

func (ka *tcpKeepalive) acceptPong(payload []byte) (time.Duration, float64, bool) {
	if len(payload) != pongPayloadLen {
		return 0, 0, false
	}
	id := binary.BigEndian.Uint64(payload[:pingPayloadLen])
	ka.mu.Lock()
	sentAt, ok := ka.pending[id]
	if ok {
		delete(ka.pending, id)
		ka.lastPong = time.Now()
	}
	ka.mu.Unlock()
	if !ok {
		return 0, 0, false
	}
	rtt := time.Since(sentAt)
	loss := float64(payload[pingPayloadLen]) / 100
	return rtt, loss, true
}

// expired reports whether a matching pong has been missing for too long.
func (ka *tcpKeepalive) expired(now time.Time) bool {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	return now.Sub(ka.lastPong) > ka.timeout
}

// startTCPKeepalive sends periodic pings and closes the connection if no matching pong arrives.
func startTCPKeepalive(ctx context.Context, conn net.Conn, interval, timeout time.Duration, send bool) (*tcpKeepalive, func()) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if timeout <= interval {
		timeout = interval * 3
	}

	ka := &tcpKeepalive{
		lastPong: time.Now(),
		timeout:  timeout,
		pending:  make(map[uint64]time.Time),
	}
	keepCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	if !send {
		return ka, func() { cancel() }
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
				payload := ka.newPing()
				logs.LogV("[keepalive] ping → %T", conn)
				if err := writeFrameSafe(conn, framePing, payload); err != nil {
					log.Printf("[keepalive] ping send failed: %v", err)
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
					log.Printf("[keepalive] timeout waiting for pong (conn=%T)", conn)
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
