package tcp

import (
	"io"
	"net"
	"testing"
	"time"
)

type fakeMedia struct{ done chan struct{} }

func (f *fakeMedia) Stop() error           { return nil }
func (f *fakeMedia) Done() <-chan struct{} { return f.done }

// closedWithin reports whether the far end of a pipe sees it close in d.
func closedWithin(peer net.Conn, d time.Duration) bool {
	_ = peer.SetReadDeadline(time.Now().Add(d))
	_, err := io.Copy(io.Discard, peer)
	return err == nil // EOF ends io.Copy without an error; a timeout is one
}

func TestHangupWhenMediaEnds(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	sess := &fakeMedia{done: make(chan struct{})}
	stop := hangupWhenMediaEnds(sess, conn)
	defer stop()
	close(sess.done) // the speaker went away, say
	if !closedWithin(peer, 2*time.Second) {
		t.Fatal("the call was not hung up when its media ended by itself")
	}
}

func TestNoHangupWhenTheCallEndsTheMedia(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	sess := &fakeMedia{done: make(chan struct{})}
	stop := hangupWhenMediaEnds(sess, conn)
	stop() // the call is ending: it stops the session itself
	close(sess.done)
	if closedWithin(peer, 300*time.Millisecond) {
		t.Fatal("hung up a call that was ending anyway")
	}
}
