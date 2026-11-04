package udp

import (
	"testing"
	"time"

	"github.com/svanichkin/say/ui"
)

func TestVideoLossMeterCountsGapsAndFailures(t *testing.T) {
	var m videoLossMeter
	start := time.Now()
	// 100 frames, every 10th lost in the network, one more fails to decode.
	for i := 0; i < 100; i++ {
		if i%10 == 5 {
			continue
		}
		m.observe(byte(i), start.Add(time.Duration(i)*10*time.Millisecond))
	}
	m.failed()
	m.report(start.Add(1100 * time.Millisecond))
	loss, at := ui.RxVideoLossSnapshot()
	if at.IsZero() || loss < 0.105 || loss > 0.115 {
		t.Fatalf("loss = %.3f, want 0.11 (10 missing + 1 undecodable of 100)", loss)
	}
	// The counter wraps at 256 without counting phantom loss.
	var w videoLossMeter
	for i := 250; i < 262; i++ {
		w.observe(byte(i), start)
	}
	if w.lost != 0 || w.expected != 12 {
		t.Fatalf("wrap-around: lost %d of %d, want 0 of 12", w.lost, w.expected)
	}
}

func TestNextVideoFPS(t *testing.T) {
	clean := 0
	fps := 25
	fps = nextVideoFPS(fps, 25, 0.20, &clean) // heavy loss
	if fps != 16 {
		t.Fatalf("heavy loss: %d fps, want 16", fps)
	}
	fps = nextVideoFPS(fps, 25, 0.05, &clean) // light loss
	if fps != 13 {
		t.Fatalf("light loss: %d fps, want 13", fps)
	}
	for i := 0; i < 40 && fps < 25; i++ { // clean path: back up to the limit
		fps = nextVideoFPS(fps, 25, 0, &clean)
	}
	if fps != 25 {
		t.Fatalf("clean path did not restore 25 fps (%d)", fps)
	}
	for i := 0; i < 10; i++ {
		fps = nextVideoFPS(fps, 25, 0.5, &clean)
	}
	if fps != minVideoFPS {
		t.Fatalf("persistent loss: %d fps, want the %d fps floor", fps, minVideoFPS)
	}
}

func TestKeyframeIntervalStaysAboutHalfASecond(t *testing.T) {
	for fps, want := range map[int]int{60: 10, 25: 10, 10: 5, 5: 2, 2: 2} {
		if got := videoKeyframeFrames(fps); got != want {
			t.Errorf("%d fps: keyframe every %d frames, want %d", fps, got, want)
		}
	}
}

func TestVideoHoldAdaptsToLossAndPictureSize(t *testing.T) {
	if maxVideoHold(800) != 32 || maxVideoHold(12800) != 128 || maxVideoHold(3700) != 80 {
		t.Fatalf("max hold: %d, %d, %d (want 32, 128, 80)", maxVideoHold(800), maxVideoHold(12800), maxVideoHold(3700))
	}
	hold := 10
	for i := 0; i < 10; i++ { // a bad path
		hold = nextVideoHold(hold, 10, maxVideoHold(3168), 0.2)
	}
	if hold != maxVideoHold(3168) {
		t.Fatalf("heavy loss: hold %d, want the %d ceiling", hold, maxVideoHold(3168))
	}
	for i := 0; i < 40; i++ { // clean again
		hold = nextVideoHold(hold, 10, maxVideoHold(3168), 0)
	}
	if hold != 10 {
		t.Fatalf("clean path: hold %d, want back at 10", hold)
	}
	if got := nextVideoHold(10, 10, 32, 0.05); got != 16 {
		t.Fatalf("light loss: hold %d, want 16", got)
	}
}
