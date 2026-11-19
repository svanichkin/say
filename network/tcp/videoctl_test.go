package tcp

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/svanichkin/say/ui"
)

// A video control message travels over the control connection to the
// peer's video sender.
func TestVideoCtlReachesThePeer(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startVideoCtl(ctx, a)
	defer stop()
	go serveControlFrames(bufio.NewReader(b), controlSession{conn: b, peerLabel: "test"})
	ui.SendVideoCtl([]byte{1, 2, 3})
	select {
	case msg := <-ui.PeerVideoCtl():
		if string(msg) != "\x01\x02\x03" {
			t.Fatalf("the peer saw %v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the peer never saw the message")
	}
}
