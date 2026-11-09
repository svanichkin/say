package tcp

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHelloRoundTrip(t *testing.T) {
	SetLocalInfo("v1.2.3 dirty", "opus")
	defer SetLocalInfo("", "")
	line := helloLine("HELLO", "my\thost")
	rest, ok := strings.CutPrefix(line, "HELLO ")
	if !ok || !strings.HasSuffix(line, "\n") {
		t.Fatalf("bad line %q", line)
	}
	got := parseHello(rest)
	want := PeerInfo{Name: "my host", Proto: ProtocolVersion, Min: MinProtocolVersion, App: "v1.2.3_dirty", AudioCodec: "opus"}
	if got != want {
		t.Fatalf("parsed %+v, want %+v", got, want)
	}
	if !compatible(got) {
		t.Fatal("this build must be compatible with itself")
	}
}

// v1.0.0 sends "HELLO <name>" and reads only "OK <name>": its lines carry
// no fields, and the name before the tab is all it sees of ours.
func TestHelloLegacyPeer(t *testing.T) {
	got := parseHello("old-host\n")
	if !got.Legacy || got.Proto != 1 || got.AudioCodec != "" || got.Name != "old-host" {
		t.Fatalf("legacy peer parsed as %+v", got)
	}
	if !compatible(got) {
		t.Fatal("protocol 1 peers are still supported")
	}
	reply := helloLine("OK", "me")
	if !strings.HasPrefix(reply, "OK me\t") {
		t.Fatalf("an old client must still see %q, got %q", "OK ", reply)
	}
}

func TestHelloIncompatible(t *testing.T) {
	if compatible(PeerInfo{Proto: ProtocolVersion + 5, Min: ProtocolVersion + 1}) {
		t.Fatal("a peer that needs a newer protocol is incompatible")
	}
	if compatible(PeerInfo{Proto: MinProtocolVersion - 1, Min: 0}) {
		t.Fatal("a peer older than MinProtocolVersion is incompatible")
	}
}

// A client gets INCOMPATIBLE from a server it cannot talk to and reports it
// with both versions instead of an "unexpected reply".
func TestClientReportsIncompatibleServer(t *testing.T) {
	SetLocalInfo("v2.0.0", "amrnb")
	defer SetLocalInfo("", "")
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		br := bufio.NewReader(remote)
		if _, err := br.ReadString('\n'); err != nil {
			return
		}
		remote.Write([]byte("INCOMPATIBLE srv\tproto=9 min=9 app=v9.0.0 codec=opus\n"))
	}()
	dial := func(string, int) (net.Conn, error) { return local, nil }
	_, _, err := StartSignalClientTCP("me", "127.0.0.1", 1, nil, nil, dial)
	var inc *IncompatibleError
	if !errors.As(err, &inc) {
		t.Fatalf("got %v, want an IncompatibleError", err)
	}
	if inc.Peer.App != "v9.0.0" || inc.Peer.Proto != 9 {
		t.Fatalf("peer info %+v", inc.Peer)
	}
	if msg := err.Error(); !strings.Contains(msg, "v9.0.0") || !strings.Contains(msg, "v2.0.0") {
		t.Fatalf("message lacks the versions: %q", msg)
	}
}

// A server answers an incompatible HELLO with INCOMPATIBLE and its own
// versions, then closes the connection.
func TestServerRejectsIncompatibleClient(t *testing.T) {
	SetLocalInfo("v2.0.0", "amrnb")
	defer SetLocalInfo("", "")
	local, remote := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serverHandleIncoming(remote, &callOwner{}, nil, nil, nil)
	}()
	local.Write([]byte("HELLO future\tproto=9 min=9 app=v9.0.0\n"))
	local.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(local).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(line, incompatibleReply+" ")
	if !ok {
		t.Fatalf("reply %q, want INCOMPATIBLE", line)
	}
	if peer := parseHello(rest); peer.Proto != ProtocolVersion || peer.App != "v2.0.0" {
		t.Fatalf("server announced %+v", peer)
	}
	local.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server kept the incompatible connection")
	}
}

// A side that shows video through a colour filter announces it; a peer of
// protocol 2 sends no such field and gets colour, as before.
func TestHelloMonoView(t *testing.T) {
	SetLocalMonoView(true)
	defer SetLocalMonoView(false)
	rest, _ := strings.CutPrefix(helloLine("HELLO", "me"), "HELLO ")
	if got := parseHello(rest); !got.MonoView {
		t.Fatalf("view=mono not announced: %+v", got)
	}
	if got := parseHello("peer\tproto=2 min=1 app=v1.0.6 codec=amrnb\n"); got.MonoView || got.Proto != 2 {
		t.Fatalf("protocol 2 peer parsed as %+v", got)
	}
}
