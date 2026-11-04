package tcp

import (
	"errors"
	"net"
	"sync"
	"time"
)

// busyReply is the handshake reply sent instead of "OK" while another call is active.
const busyReply = "BUSY"

// ErrPeerBusy is returned by the client when the peer is already in another call.
var ErrPeerBusy = errors.New("peer is busy with another call")

// callTakeoverWait bounds how long a reconnecting peer waits for its previous,
// stale call to release the media session.
const callTakeoverWait = 5 * time.Second

// activeCall is the call currently owning the UDP socket, devices and UI.
type activeCall struct {
	host string
	conn net.Conn
	done chan struct{}
}

// callOwner admits one call at a time. All calls share one UDP socket and the
// global UI and audio/video devices, so concurrent sessions would compete for
// packets and devices.
type callOwner struct {
	mu     sync.Mutex
	active *activeCall
}

// acquire makes the connection from host the active call. A new connection
// from the host that owns the current call replaces it, since that is a
// reconnect while the old connection has not timed out yet. Any other host
// gets ok=false. On success the caller must call release when the call ends.
func (o *callOwner) acquire(host string, conn net.Conn) (release func(), ok bool) {
	for {
		o.mu.Lock()
		prev := o.active
		if prev == nil {
			call := &activeCall{host: host, conn: conn, done: make(chan struct{})}
			o.active = call
			o.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					o.mu.Lock()
					if o.active == call {
						o.active = nil
					}
					o.mu.Unlock()
					close(call.done)
				})
			}, true
		}
		o.mu.Unlock()

		if prev.host != host {
			return nil, false
		}
		_ = prev.conn.Close()
		select {
		case <-prev.done:
			// The stale call released its media; try again.
		case <-time.After(callTakeoverWait):
			return nil, false
		}
	}
}

// hangupActive ends the active call, if any, telling the peer.
func (o *callOwner) hangupActive() {
	o.mu.Lock()
	call := o.active
	o.mu.Unlock()
	if call != nil {
		_ = hangup(call.conn)
	}
}
