package tcp

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/ui"
)

// After the handshake the server sends its card (frame 0x12); a card frame
// from the peer goes to the book's handler with the address it came from.
func TestCardsExchanged(t *testing.T) {
	got := make(chan string, 1)
	SetCards(func() []byte { return []byte("CARD-OF-SERVER") }, func(host string, card []byte) { got <- host + "|" + string(card) })
	defer SetCards(nil, nil)
	SetLocalInfo("v2.0.0", "amrnb")
	defer SetLocalInfo("", "")
	local, remote := net.Pipe()
	defer local.Close()
	go serverHandleIncoming(&framedConn{Conn: remote}, &callOwner{}, nil, nil, nil)
	fmt.Fprintf(local, "HELLO peer\tproto=%d min=%d app=v2.0.0\n", ProtocolVersion, MinProtocolVersion)
	local.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(local)
	if line, err := br.ReadString('\n'); err != nil || !strings.HasPrefix(line, ringReply+" ") {
		t.Fatal(line, err)
	}
	ui.KeyInput('\r') // answered (ring.go)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	ty, payload, err := readFrame(br)
	if err != nil || ty != frameCard || string(payload) != "CARD-OF-SERVER" {
		t.Fatalf("after the handshake: %x %q %v", ty, payload, err)
	}
	go func() {
		for { // the rest the server sends (pings and the like)
			if _, _, err := readFrame(br); err != nil {
				return
			}
		}
	}()
	if err := writeFrame(local, frameCard, []byte("CARD-OF-PEER")); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "pipe|CARD-OF-PEER" && s[len(s)-len("|CARD-OF-PEER"):] != "|CARD-OF-PEER" {
			t.Fatalf("the peer's card: %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the peer's card not handed on")
	}
}
