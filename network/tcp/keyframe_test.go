package tcp

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/svanichkin/say/ui"
)

// A keyframe request travels over the control connection to the peer's
// video sender.
func TestKeyframeRequestReachesThePeer(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ui.TakePeerKeyframeRequest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startKeyframeRequester(ctx, a)
	defer stop()
	go serveControlFrames(bufio.NewReader(b), controlSession{conn: b, peerLabel: "test"})
	ui.RequestKeyframe()
	deadline := time.Now().Add(2 * time.Second)
	for !ui.TakePeerKeyframeRequest() {
		if time.Now().After(deadline) {
			t.Fatal("the peer never saw the keyframe request")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
