package tcp

// DEBUG-TEMP: with logforward.go.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/logs"
)

func TestPeerLogArrives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.log")
	logs.SetPeerLogPath(path)
	defer logs.SetPeerLogPath("")
	w := logs.Tap(io.Discard)
	for i := range 3 {
		fmt.Fprintf(w, "line %d of the callee\n", i)
	}

	caller, callee := net.Pipe()
	defer caller.Close()
	defer callee.Close()
	go serveControlFrames(bufio.NewReader(callee), controlSession{conn: callee, peerLabel: "caller"})
	go serveControlFrames(bufio.NewReader(caller), controlSession{conn: caller, peerLabel: "callee"})
	if err := writeFrameSafe(caller, frameLogWant, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "line 2 of the callee") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(path)
	t.Fatalf("the callee's log did not arrive: %q", b)
}

func TestLogRingDropsAndNotes(t *testing.T) {
	w := logs.Tap(io.Discard)
	start := logs.Oldest()
	fmt.Fprintf(w, "a\n")
	chunk, next := logs.Since(start, 1<<20)
	if !strings.Contains(string(chunk), "a\n") || next <= start {
		t.Fatalf("Since = %q, %d", chunk, next)
	}
}

func TestDebugLinesKeptWithoutVerbose(t *testing.T) {
	start := logs.Oldest()
	logs.LogV("[test] a debug line %d", 7)
	chunk, _ := logs.Since(start, 1<<20)
	if !strings.Contains(string(chunk), "[test] a debug line 7") {
		t.Fatalf("debug line not kept without -log debug: %q", chunk)
	}
}
