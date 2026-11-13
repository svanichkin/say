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
	pingPayloadLen    = 8
	pongPayloadLen    = 10 // ping id, audio loss %, video loss %
	pongLegacyLen     = 9  // older peers report audio loss only
	keepaliveInterval = 2 * time.Second
	keepaliveTimeout  = 30 * time.Second
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
	ui.NotePingsOut(ka.oldestPendingLocked())
	payload := make([]byte, pingPayloadLen)
	binary.BigEndian.PutUint64(payload, id)
	return payload
}

// oldestPendingLocked is when the oldest unanswered ping was sent (zero:
// none). ka.mu is held.
func (ka *tcpKeepalive) oldestPendingLocked() time.Time {
	var oldest time.Time
	for _, at := range ka.pending {
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	return oldest
}

func (ka *tcpKeepalive) pongPayload(ping []byte) []byte {
	if len(ping) != pingPayloadLen {
		return nil
	}
	payload := make([]byte, pongPayloadLen)
	copy(payload, ping)
	payload[pingPayloadLen] = lossPercent(ui.RxAudioLossSnapshot())
	payload[pingPayloadLen+1] = lossPercent(ui.RxVideoLossSnapshot())
	return payload
}

// lossPercent encodes a recent loss estimate as 0..100; stale data reads as 0.
func lossPercent(loss float64, updatedAt time.Time) byte {
	if updatedAt.IsZero() || time.Since(updatedAt) > 3*time.Second {
		return 0
	}
	return byte(min(max(math.Round(loss*100), 0), 100))
}

// acceptPong matches a pong to its ping. It returns the RTT and the peer's
// loss of our audio; the peer's loss of our video goes to ui.UpdatePeerVideoLoss.
func (ka *tcpKeepalive) acceptPong(payload []byte) (time.Duration, float64, bool) {
	if len(payload) != pongPayloadLen && len(payload) != pongLegacyLen {
		return 0, 0, false
	}
	id := binary.BigEndian.Uint64(payload[:pingPayloadLen])
	ka.mu.Lock()
	sentAt, ok := ka.pending[id]
	if ok {
		delete(ka.pending, id)
		ka.lastPong = time.Now()
		// Pongs come back in order on the stream: earlier pings still
		// pending were never answered and must not count as out.
		for pid, at := range ka.pending {
			if !at.After(sentAt) {
				delete(ka.pending, pid)
			}
		}
		ui.NotePingsOut(ka.oldestPendingLocked())
	}
	ka.mu.Unlock()
	if !ok {
		return 0, 0, false
	}
	rtt := time.Since(sentAt)
	loss := float64(payload[pingPayloadLen]) / 100
	if len(payload) == pongPayloadLen {
		ui.UpdatePeerVideoLoss(float64(payload[pingPayloadLen+1]) / 100)
	}
	return rtt, loss, true
}

// expired reports whether a matching pong has been missing for too long.
func (ka *tcpKeepalive) expired(now time.Time) bool {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	return now.Sub(ka.lastPong) > ka.timeout
}

// startTCPKeepalive sends periodic pings and closes the connection if no matching pong arrives.
// While fast reports true (a file is being sent) it pings every
// fileProbeInterval: the file pacer measures its queue with these pings.
func startTCPKeepalive(ctx context.Context, conn net.Conn, interval, timeout time.Duration, send bool, fast func() bool) (*tcpKeepalive, func()) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if timeout <= interval {
		timeout = interval * 3
	}

	ui.ResetPathQueue() // a new call: its own usual round trip
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
		tick := min(interval, fileProbeInterval)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		var lastPing time.Time
		for {
			select {
			case <-keepCtx.Done():
				return
			case now := <-ticker.C:
				if (fast == nil || !fast()) && now.Sub(lastPing) < interval-tick/2 {
					continue
				}
				lastPing = now
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
