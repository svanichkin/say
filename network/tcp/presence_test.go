package tcp

import (
	"errors"
	"net"
	"testing"
	"time"
)

// A probe is answered PONG and takes no call; an address where nothing
// answers is not online.
func TestProbe(t *testing.T) {
	SetLocalInfo("v2.0.0", "amrnb")
	defer SetLocalInfo("", "")
	owner := &callOwner{}
	dial := func(string, int) (net.Conn, error) {
		local, remote := net.Pipe()
		go serverHandleIncoming(&framedConn{Conn: remote}, owner, nil, nil, nil)
		return local, nil
	}
	if !Probe("200::1", 7777, dial, 3*time.Second) {
		t.Fatal("Say did not answer the probe")
	}
	owner.mu.Lock()
	active := owner.active
	owner.mu.Unlock()
	if active != nil {
		t.Fatal("a probe took the line as a call")
	}
	refused := func(string, int) (net.Conn, error) { return nil, errors.New("no route") }
	if Probe("200::2", 7777, refused, time.Second) {
		t.Fatal("nothing there, yet online")
	}
	silent := func(string, int) (net.Conn, error) { // connects, never answers
		local, _ := net.Pipe()
		return local, nil
	}
	start := time.Now()
	if Probe("200::3", 7777, silent, 200*time.Millisecond) || time.Since(start) > time.Second {
		t.Fatal("a silent address is online, or the probe did not give up in time")
	}
}
