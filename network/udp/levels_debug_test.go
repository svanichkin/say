package udp

// DEBUG-TEMP: with levels_debug.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
)

func TestLevelMeterLogsEachSecond(t *testing.T) {
	m := levelMeter{name: "test"}
	loud := make([]int16, 160)
	for i := range loud {
		loud[i] = 16384
	}
	m.add(loud, true)
	m.since = time.Now().Add(-2 * time.Second)
	m.add(loud, false)
	if m.frames != 0 || m.speech != 0 {
		t.Fatalf("the meter did not start a new second: %+v", m)
	}
	if got := dbfs(16384); got != "-6 dBFS" {
		t.Fatalf("dbfs(16384) = %q, want -6 dBFS", got)
	}
	if got := dbfs(0); got != "silent" {
		t.Fatalf("dbfs(0) = %q", got)
	}
}

// A frame's line joins what the network and the decoder saw of it.
func TestVoiceTraceJoinsAFramesStory(t *testing.T) {
	start := logs.Oldest()
	traceVoiceRx(1005, "video", 14)
	traceVoiceDecoded(1005, make([]int16, 160), 320, false)
	logPlayEvent(device.PlayEvent{Kind: device.PlayStarted, Index: 1005, At: time.Now(), Samples: 160})
	traceVoiceDecoded(1010, make([]int16, 160), 0, true)
	logPlayEvent(device.PlayEvent{Kind: device.PlayStarted, Index: 1010, At: time.Now(), Samples: 160})
	logPlayEvent(device.PlayEvent{Kind: device.PlayDry, Index: 1010, At: time.Now(), Samples: 80})
	chunk, _ := logs.Since(start, 1<<20)
	for _, want := range []string{
		"voice #1005 playing (20 ms, silent): arrived",
		"by video datagram, 14 B; 40 ms queued ahead of it",
		"voice #1010 playing (20 ms, silent): made up for a missing frame",
		"speaker ran dry after voice #1010: 10 ms of silence",
	} {
		if !strings.Contains(string(chunk), want) {
			t.Errorf("no %q in:\n%s", want, chunk)
		}
	}
}
