package device

import (
	"testing"
	"time"
)

func resetTerminalFlow() {
	terminalFlowOn.Store(false)
	terminalProbes.Store(0)
	terminalPending.Store(0)
	terminalProgress.Store(0)
	terminalAcks.Store(0)
	terminalAckNanos.Store(0)
}

func TestTerminalFlowNeverWaitsForSilentTerminal(t *testing.T) {
	resetTerminalFlow()
	defer resetTerminalFlow()
	queries := 0
	for i := 0; i < 50; i++ {
		if terminalQueryAfterFrame() != "" {
			queries++
		}
		if TerminalBusy() {
			t.Fatal("waited for a terminal that never answered")
		}
	}
	if queries != terminalProbeFrames {
		t.Fatalf("%d probes, want %d", queries, terminalProbeFrames)
	}
}

func TestTerminalFlowWaitsForEveryOutstandingAnswer(t *testing.T) {
	resetTerminalFlow()
	defer resetTerminalFlow()
	// Three frames written before the terminal's first answer.
	for i := 0; i < 3; i++ {
		terminalQueryAfterFrame()
	}
	TerminalAck()
	if !TerminalBusy() {
		t.Fatal("caught up after the first of three answers")
	}
	TerminalAck()
	TerminalAck()
	if TerminalBusy() {
		t.Fatal("still busy after all answers")
	}
	if terminalQueryAfterFrame() == "" || !TerminalBusy() {
		t.Fatal("a frame after the first answer does not wait for its own")
	}
	TerminalAck()
	if TerminalBusy() {
		t.Fatal("busy after the answer")
	}
	TerminalAck() // a stray answer must not let a later frame skip its wait
	terminalQueryAfterFrame()
	if !TerminalBusy() {
		t.Fatal("stray answer cancelled the next wait")
	}
}

func TestTerminalFlowLostAnswerTimesOut(t *testing.T) {
	resetTerminalFlow()
	defer resetTerminalFlow()
	terminalQueryAfterFrame()
	TerminalAck()
	terminalQueryAfterFrame()
	terminalProgress.Store(time.Now().Add(-terminalAckTimeout - time.Millisecond).UnixNano())
	if TerminalBusy() {
		t.Fatal("waits forever for a lost answer")
	}
	if terminalPending.Load() != 0 {
		t.Fatal("lost answers still counted")
	}
}
