package tcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/keys"
	"github.com/svanichkin/say/ui"
)

func init() { playTone = func([]int16, int, bool) func() { return func() {} } } // silent tests

// ringServer listens on localhost as Say does; it returns the address.
func ringServer(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop, err := StartSignalServerTCP(ln, nil, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop() })
	return ln.Addr().String()
}

// call says HELLO as a caller of proto does and returns the first reply.
func call(t *testing.T, addr string, proto int) (net.Conn, *bufio.Reader, string) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "HELLO them\tproto=%d min=14\n", proto)
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return conn, br, line
}

func waitFree(t *testing.T) {
	for end := time.Now().Add(3 * time.Second); InCall(); {
		if time.Now().After(end) {
			t.Fatal("the line is still taken")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A caller of 17 hears RING, then OK when ✓ (Enter) answers, DECLINED when
// x (Esc) declines; the line is taken meanwhile (another address gets
// BUSY, as in a call); a caller of 16 gets OK at once; a caller hanging up
// while it rings frees the line.
func TestRingAnswerDecline(t *testing.T) {
	addr := ringServer(t)
	for _, tc := range []struct {
		key  rune
		want string
	}{{'\r', "OK "}, {keys.Esc, declinedReply + " "}} {
		conn, br, line := call(t, addr, ringProto)
		if !strings.HasPrefix(line, ringReply+" ") {
			t.Fatalf("first reply %q, want RING", line)
		}
		if !InCall() {
			t.Fatal("the line is free while it rings: a third caller would not get BUSY")
		}
		ui.KeyInput(tc.key)
		line, err := br.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, tc.want) {
			t.Fatalf("answer %q (%v), want %q", line, err, tc.want)
		}
		conn.Close()
		waitFree(t)
	}

	conn, _, line := call(t, addr, 16)
	if !strings.HasPrefix(line, "OK ") {
		t.Fatalf("a caller of 16 got %q", line)
	}
	conn.Close()
	waitFree(t)

	conn, _, _ = call(t, addr, ringProto)
	conn.Close() // hung up while ringing
	waitFree(t)
	ui.KeyInput('\r') // nothing rings: answers nothing
}

// The caller: RING tells it the call rings; DECLINED is ErrDeclined; its
// context cancelled while ringing closes the line at once.
func TestClientRinging(t *testing.T) {
	for _, declined := range []bool{true, false} {
		local, remote := net.Pipe()
		go func() {
			br := bufio.NewReader(remote)
			if _, err := br.ReadString('\n'); err != nil {
				return
			}
			fmt.Fprint(remote, helloLine(ringReply, "peer"))
			if declined {
				fmt.Fprint(remote, helloLine(declinedReply, "peer"))
			}
			_, _ = br.ReadString('\n') // until the caller closes
			remote.Close()
		}()
		ctx, cancel := context.WithCancel(context.Background())
		rang := make(chan struct{})
		dial := func(string, int) (net.Conn, error) { return local, nil }
		if !declined {
			go func() { <-rang; cancel() }() // hung up while it rings
		}
		start := time.Now()
		_, _, _, err := StartSignalClientTCP(ctx, "me", "200::1", 7777, nil, nil, dial, func() { close(rang) })
		want := ErrDeclined
		if !declined {
			want = context.Canceled
		}
		if !errors.Is(err, want) || time.Since(start) > 2*time.Second {
			t.Fatalf("declined %v: %v after %v", declined, err, time.Since(start))
		}
		cancel()
	}
}
