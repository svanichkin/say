package ui

import (
	"strings"
	"testing"
	"time"
)

func TestPanesShowNoSignalNoiseThenTestCard(t *testing.T) {
	defer updateNoiseAnimation(false)
	vs, ok := calcViewportSlots(120, 40)
	if !ok {
		t.Fatal("no layout")
	}
	start := time.Now()
	var out strings.Builder
	if !writeChatPane(&out, nil, time.Time{}, remoteFrameType, "NO SIGNAL NET", start, 2, 2, vs.peerSlot) {
		t.Fatal("pane without frames is not idle")
	}
	if !strings.Contains(out.String(), "NO SIGNAL NET") {
		t.Fatal("no NO SIGNAL NET label at call start")
	}
	if updateIdlePatternSlot(remoteFrameType, true, start.Add(time.Second)) {
		t.Fatal("test card before testCardDelay")
	}
	if !updateIdlePatternSlot(remoteFrameType, true, start.Add(testCardDelay+time.Millisecond)) {
		t.Fatal("no test card after testCardDelay")
	}
	// A live frame clears the idle state.
	frame := noiseTextFrame(vs.peerSlot.cols, vs.peerSlot.rows)
	out.Reset()
	if writeChatPane(&out, frame, start.Add(4*time.Second), remoteFrameType, "NO SIGNAL NET", start.Add(4*time.Second), 2, 2, vs.peerSlot) {
		t.Fatal("pane with a fresh frame is idle")
	}
	if strings.Contains(out.String(), noSignalLabel) {
		t.Fatal("NO SIGNAL shown over a live frame")
	}
	// Frames stop: idle again after frameFreezeThreshold.
	if !writeChatPane(&out, frame, start.Add(4*time.Second), remoteFrameType, "NO SIGNAL NET", start.Add(4*time.Second+frameFreezeThreshold+time.Millisecond), 2, 2, vs.peerSlot) {
		t.Fatal("frozen pane is not idle")
	}
}
