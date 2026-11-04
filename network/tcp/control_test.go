package tcp

import (
	"bufio"
	"net"
	"testing"
	"time"
)

func frameBytes(t *testing.T, cols, rows int) []byte {
	t.Helper()
	payload, ok := encodeTermSizeFrame(cols, rows)
	if !ok {
		t.Fatal("encodeTermSizeFrame failed")
	}
	return append([]byte{frameTermSize, byte(len(payload) >> 8), byte(len(payload))}, payload...)
}

// The handshake reader may buffer frames that arrive in the same segment as
// "OK"; they must reach the frame loop, and a frame split by a long pause
// must still be parsed intact.
func TestControlStreamKeepsHandshakeBufferAndPartialFrames(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()

	got := make(chan [2]int, 4)
	sync := &TermSizeSync{OnPeer: func(cols, rows int) { got <- [2]int{cols, rows} }}

	go func() {
		defer remote.Close()
		first := append([]byte("OK peer\n"), frameBytes(t, 80, 24)...)
		_, _ = remote.Write(first)
		second := frameBytes(t, 120, 40)
		_, _ = remote.Write(second[:5]) // header plus part of the payload
		time.Sleep(700 * time.Millisecond)
		_, _ = remote.Write(second[5:])
	}()

	br := bufio.NewReader(local)
	line, err := br.ReadString('\n')
	if err != nil || line != "OK peer\n" {
		t.Fatalf("handshake line %q, err %v", line, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveControlFrames(br, controlSession{conn: local, termSync: sync, peerLabel: "test"})
	}()

	for _, want := range [][2]int{{80, 24}, {120, 40}} {
		select {
		case size := <-got:
			if size != want {
				t.Fatalf("got term size %v, want %v", size, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("term size %v was lost", want)
		}
	}
	<-done
}
