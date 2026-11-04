package tcp

import (
	"bufio"
	"net"
	"testing"
	"time"
)

// A peer that hangs up (Ctrl+C) must end the call on the other side at once,
// not after the keepalive timeout. net.Pipe does not deliver a close to the
// reader by itself here: the goodbye frame alone must end the loop.
func TestHangupEndsPeerCallAtOnce(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveControlFrames(bufio.NewReader(a), controlSession{conn: a, peerLabel: "test"})
	}()
	start := time.Now()
	go func() {
		_ = writeFrameSafe(b, frameBye, nil) // what hangup sends first
	}()
	select {
	case <-done:
		if d := time.Since(start); d > time.Second {
			t.Fatalf("call ended after %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goodbye frame did not end the call")
	}
	b.Close()
}
