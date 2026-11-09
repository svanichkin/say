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
	for fps, want := range map[int]int{60: 30, 25: 12, 10: 5, 5: 2, 2: 2} {
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

func TestVideoAdaptRaisesHoldBeforeLoweringFPS(t *testing.T) {
	const ceiling, base, maxHold = 25, 10, 80
	a := videoAdapt{fps: ceiling, hold: base}
	// Loss: the hold climbs to its maximum while the frame rate stays put.
	for i := 0; a.hold < maxHold; i++ {
		if i > 20 {
			t.Fatalf("hold stuck at %d", a.hold)
		}
		a.update(0.2, ceiling, ceiling, base, maxHold)
		if a.fps != ceiling {
			t.Fatalf("fps lowered to %d while hold was %d < %d", a.fps, a.hold, maxHold)
		}
	}
	// Only then the frame rate drops.
	a.update(0.2, ceiling, ceiling, base, maxHold)
	if a.fps >= ceiling || a.hold != maxHold {
		t.Fatalf("hold exhausted: fps %d, hold %d; want fps below %d", a.fps, a.hold, ceiling)
	}
	a.update(0.05, a.fps, ceiling, base, maxHold)
	low := a.fps
	// Clean path: the frame rate comes back first, the detail after it.
	for i := 0; a.fps < ceiling; i++ {
		if i > 100 {
			t.Fatalf("fps stuck at %d", a.fps)
		}
		if a.hold != maxHold {
			t.Fatalf("hold lowered to %d at %d fps (from %d), before fps was restored", a.hold, a.fps, low)
		}
		a.update(0, a.fps, ceiling, base, maxHold)
	}
	for i := 0; i < 40; i++ {
		a.update(0, a.fps, ceiling, base, maxHold)
	}
	if a.fps != ceiling || a.hold != base {
		t.Fatalf("clean path: fps %d, hold %d; want %d, %d", a.fps, a.hold, ceiling, base)
	}
}

func TestVideoAdaptCleanReportNeverLowersFPS(t *testing.T) {
	a := videoAdapt{fps: 60, hold: 10}
	// A busy machine: the camera delivered only 7 frames, no loss at all.
	a.update(0, 7, 60, 10, 80)
	if a.fps != 60 {
		t.Fatalf("clean report with 7 frames sent: %d fps, want 60", a.fps)
	}
	a.update(0.02, 7, 60, 10, 80) // between clean and light loss
	if a.fps != 60 {
		t.Fatalf("2%% loss with 7 frames sent: %d fps, want 60", a.fps)
	}
}

func TestVideoAdaptDropsColourFirstAndRestoresItLast(t *testing.T) {
	const ceiling, base, maxHold = 25, 10, 80
	a := videoAdapt{fps: ceiling, hold: base}
	a.update(0.05, ceiling, ceiling, base, maxHold)
	if !a.noColour || a.hold != base || a.fps != ceiling {
		t.Fatalf("first loss: colour off %v, hold %d, fps %d; want colour off only", a.noColour, a.hold, a.fps)
	}
	a.update(0.05, ceiling, ceiling, base, maxHold)
	if a.hold == base || a.fps != ceiling {
		t.Fatalf("second loss: hold %d, fps %d; want the hold raised", a.hold, a.fps)
	}
	// Clean: the hold comes back first, colour only after a longer clean spell.
	for i := 0; a.hold > base; i++ {
		if i > 40 {
			t.Fatalf("hold stuck at %d", a.hold)
		}
		a.update(0, ceiling, ceiling, base, maxHold)
		if !a.noColour {
			t.Fatalf("colour back with hold %d above %d", a.hold, base)
		}
	}
	for i := 1; i < videoColourCleanReports; i++ {
		a.update(0, ceiling, ceiling, base, maxHold)
		if !a.noColour {
			t.Fatalf("colour back after %d clean reports, want %d", i, videoColourCleanReports)
		}
	}
	a.update(0.02, ceiling, ceiling, base, maxHold) // not clean: the count starts over
	for i := 0; i < videoColourCleanReports; i++ {
		a.update(0, ceiling, ceiling, base, maxHold)
	}
	if a.noColour {
		t.Fatal("colour not restored on a clean path")
	}
}
