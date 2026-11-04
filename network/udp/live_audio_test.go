package udp

import (
	"strings"
	"testing"

	"github.com/svanichkin/say/device"
)

func TestApplyAudioSettingsLive(t *testing.T) {
	defer SetSpeakerBuffer(80, 4)
	defer SetJitterLead(120)
	SetSpeakerBuffer(60, 2)
	SetJitterLead(120)

	// A fake session: the playout goroutine serves commands, the speaker restart is recorded.
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(toneDecoder{frame: 160}, 160, q)
	p.ready = true
	cmds := make(chan func(*audioPlayout), 4)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case f := <-cmds:
				f(p)
			case <-stop:
				return
			}
		}
	}()
	var restarts [][2]int
	l := &liveAudio{playoutCmds: cmds, stopCh: stop, restartSpeaker: func(pf, n int) error {
		restarts = append(restarts, [2]int{pf, n})
		return nil
	}}
	registerLiveAudio(l)
	defer unregisterLiveAudio(l)

	if s := AudioSettings(); !strings.Contains(s, "lead 120 ms, speaker 60 ms x 2") || !strings.Contains(s, "lead now 120 ms") {
		t.Fatalf("settings text: %q", s)
	}
	// Only the lead changes: no speaker restart, the playout grows its lead.
	if _, err := ApplyAudioSettings(200, 60, 2); err != nil {
		t.Fatal(err)
	}
	if len(restarts) != 0 {
		t.Fatalf("speaker restarted for a lead change: %v", restarts)
	}
	l.run(func(*audioPlayout) {}) // wait for the playout goroutine
	if p.lead != 10 || p.expand != 4 {
		t.Fatalf("lead %d frames, expand %d; want 10 and 4", p.lead, p.expand)
	}
	// The speaker buffer changes: one restart with the new period and count.
	if _, err := ApplyAudioSettings(200, 40, 3); err != nil {
		t.Fatal(err)
	}
	if len(restarts) != 1 || restarts[0] != [2]int{2, 3} {
		t.Fatalf("restarts %v, want [[2 3]]", restarts)
	}
	l.run(func(*audioPlayout) {})
	if p.lowWater != 2*160 || p.leadMin != 4 {
		t.Fatalf("after 40 ms x 3: low water %d, min lead %d", p.lowWater, p.leadMin)
	}
	if _, err := ApplyAudioSettings(0, 60, 2); err == nil {
		t.Fatal("zero lead accepted")
	}
}
