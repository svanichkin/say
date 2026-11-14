//go:build !debug

package logs

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/svanichkin/say/conf"
)

// A release build writes no [debug] or [trace] lines, even with -log
// debug, and keeps nothing to send a peer; other lines are written.
func TestReleaseBuildHasNoDebugLines(t *testing.T) {
	var out bytes.Buffer
	l := log.New(Tap(&out), "", log.LstdFlags|log.Lmicroseconds)
	l.Printf("[debug] screen 96x45: call")
	l.Printf("[trace] voice #5 playing")
	l.Printf("[p2p] connected")
	if s := out.String(); strings.Contains(s, "[debug]") || strings.Contains(s, "[trace]") || !strings.Contains(s, "[p2p] connected") {
		t.Fatalf("written: %q", s)
	}
	was := conf.Verbose
	defer func() { conf.Verbose = was }()
	conf.Verbose = false
	first := Oldest()
	LogV("[trace] voice #6 encoded")
	if chunk, _ := Since(first, 1<<20); len(chunk) != 0 {
		t.Fatalf("a release build kept %q", chunk)
	}
}
