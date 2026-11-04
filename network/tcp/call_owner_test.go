package tcp

import (
	"net"
	"testing"
	"time"
)

func TestCallOwnerAdmitsOneCall(t *testing.T) {
	var owner callOwner
	a, aPeer := net.Pipe()
	defer aPeer.Close()

	releaseA, ok := owner.acquire("10.0.0.1", a)
	if !ok {
		t.Fatal("first call was rejected")
	}

	b, bPeer := net.Pipe()
	defer b.Close()
	defer bPeer.Close()
	if _, ok := owner.acquire("10.0.0.2", b); ok {
		t.Fatal("second host was admitted while a call is active")
	}

	// A reconnect from the active host closes the stale connection and takes
	// over once the old call has released its media.
	go func() {
		buf := make([]byte, 1)
		_, _ = a.Read(buf) // unblocks when acquire closes the stale conn
		time.Sleep(50 * time.Millisecond)
		releaseA()
	}()
	c, cPeer := net.Pipe()
	defer c.Close()
	defer cPeer.Close()
	releaseC, ok := owner.acquire("10.0.0.1", c)
	if !ok {
		t.Fatal("reconnect from the active host was rejected")
	}
	if _, ok := owner.acquire("10.0.0.2", b); ok {
		t.Fatal("other host admitted during the reconnected call")
	}

	releaseC()
	releaseC() // release is idempotent
	releaseB, ok := owner.acquire("10.0.0.2", b)
	if !ok {
		t.Fatal("call was rejected after the line became free")
	}
	releaseB()
}
