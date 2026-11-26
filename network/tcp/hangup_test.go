package tcp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// A peer that hangs up (its bye) is told apart from a line that drops: the
// caller is not to redial the one, and is the other.
func TestClientKnowsThePeerHungUp(t *testing.T) {
	for _, bye := range []bool{true, false} {
		local, remote := net.Pipe()
		go func() {
			br := bufio.NewReader(remote)
			if _, err := br.ReadString('\n'); err != nil {
				return
			}
			fmt.Fprintf(remote, "OK srv\tproto=%d min=%d app=v2.0.0\n", ProtocolVersion, MinProtocolVersion)
			go func() { // what the client sends
				for {
					if _, _, err := readFrame(br); err != nil {
						return
					}
				}
			}()
			time.Sleep(50 * time.Millisecond)
			if bye {
				_ = writeFrame(remote, frameBye, nil)
			}
			remote.Close()
		}()
		dial := func(string, int) (net.Conn, error) { return local, nil }
		stop, done, hungUp, err := StartSignalClientTCP(context.Background(), "me", "200::1", 7777, nil, nil, dial, nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("the call did not end")
		}
		stop()
		if hungUp() != bye {
			t.Fatalf("bye %v: hung up %v", bye, hungUp())
		}
	}
}
