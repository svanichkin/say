package udp

import (
	"sync"
	"testing"
	"time"

	"github.com/svanichkin/say/codec"
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

// Frames that arrive after their sound has played are never shown; the
// loss report must count them, or the sender never adapts to a path that
// delivers video late.
func TestVideoReceiverReportsLateFramesAsLoss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lagTicks int32 // how far behind the playback clock frames arrive
		minLoss  float64
		maxLoss  float64
	}{
		{"on time", -20, 0, 0.01},
		{"two seconds late", 500, 0.9, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			clock := func() (uint16, bool) {
				return uint16(time.Since(start)/mediaTickDuration+1000) & mediaIndexMask, true
			}
			var wg sync.WaitGroup
			stop := make(chan struct{})
			in := make(chan []byte, 8)
			out := make(chan *codec.TextFrame, 64)
			go func() {
				for range out {
				}
			}()
			ui.UpdateRxVideoLoss(0)
			startVideoReceiver(&wg, stop, in, out, clock)
			enc := codec.NewEncoder(false, 10, 0, false)
			img := make([]byte, 64*64*3)
			for i := range img {
				img[i] = byte(i)
			}
			deadline := time.Now().Add(1300 * time.Millisecond)
			for seq := 0; time.Now().Before(deadline); seq++ {
				payload, err := enc.Encode(codec.Frame{Data: img, Width: 64, Height: 64}, 16, 8)
				if err != nil {
					t.Fatal(err)
				}
				now, _ := clock()
				index := advanceMediaIndex(now, -tc.lagTicks)
				pkt := append([]byte{byte(index >> 8), byte(index), byte(seq)}, payload...)
				in <- pkt
				time.Sleep(33 * time.Millisecond)
			}
			close(stop)
			wg.Wait()
			close(out)
			loss, at := ui.RxVideoLossSnapshot()
			if at.Before(start) || loss < tc.minLoss || loss > tc.maxLoss {
				t.Fatalf("reported loss %.2f (at %v), want %.2f..%.2f", loss, at, tc.minLoss, tc.maxLoss)
			}
		})
	}
}

// Without an audio clock (the peer's microphone is off) frames that arrive
// in a burst are skipped for the newest one. The path delivers unevenly;
// that is not loss, and a smaller stream would not change it.
func TestVideoReceiverBurstsAreNotLoss(t *testing.T) {
	start := time.Now()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	in := make(chan []byte, 8)
	out := make(chan *codec.TextFrame, 64)
	go func() {
		for range out {
		}
	}()
	ui.UpdateRxVideoLoss(0)
	startVideoReceiver(&wg, stop, in, out, func() (uint16, bool) { return 0, false })
	enc := codec.NewEncoder(false, 10, 0, false)
	img := make([]byte, 64*64*3)
	for i := range img {
		img[i] = byte(i * 7)
	}
	deadline := time.Now().Add(1300 * time.Millisecond)
	for seq := 0; time.Now().Before(deadline); {
		for k := 0; k < 3; k++ { // three frames at once, then a pause
			payload, err := enc.Encode(codec.Frame{Data: img, Width: 64, Height: 64}, 16, 8)
			if err != nil {
				t.Fatal(err)
			}
			index := uint16(time.Since(start)/mediaTickDuration) & mediaIndexMask
			in <- append([]byte{byte(index >> 8), byte(index), byte(seq)}, payload...)
			seq++
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	close(out)
	if loss, at := ui.RxVideoLossSnapshot(); at.Before(start) || loss > 0.01 {
		t.Fatalf("bursts reported as %.2f loss (at %v), want 0", loss, at)
	}
}

// A gap in the frame counter asks the peer for a keyframe, and asks again
// only after videoKeyframeRetry while frames keep failing.
func TestVideoReceiverRequestsKeyframeOnLoss(t *testing.T) {
	for len(ui.KeyframeRequests()) > 0 {
		<-ui.KeyframeRequests()
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	in := make(chan []byte, 64)
	out := make(chan *codec.TextFrame, 64)
	go func() {
		for range out {
		}
	}()
	startVideoReceiver(&wg, stop, in, out, nil)
	enc := codec.NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(10)
	img := make([]byte, 64*64*3)
	for i := range img {
		img[i] = byte(i * 3)
	}
	send := func(seq int) {
		for i := 0; i < 8*64*3; i++ { // a small part changes: change frames
			img[i] = byte(i*3 + seq*40)
		}
		payload, err := enc.Encode(codec.Frame{Data: img, Width: 64, Height: 64}, 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		in <- append([]byte{0, byte(seq), byte(seq)}, payload...)
	}
	requests := func() int {
		time.Sleep(50 * time.Millisecond)
		n := 0
		for len(ui.KeyframeRequests()) > 0 {
			<-ui.KeyframeRequests()
			n++
		}
		return n
	}
	send(0)
	send(1)
	if n := requests(); n != 0 {
		t.Fatalf("%d keyframe requests without loss", n)
	}
	send(3) // frame 2 lost
	if n := requests(); n != 1 {
		t.Fatalf("%d keyframe requests after a loss, want 1", n)
	}
	send(4) // still no base: within the retry time, no new request
	if n := requests(); n != 0 {
		t.Fatalf("asked again %d times within %v", n, videoKeyframeRetry)
	}
	time.Sleep(videoKeyframeRetry)
	send(5)
	if n := requests(); n != 1 {
		t.Fatalf("%d requests after the retry time, want 1", n)
	}
	close(stop)
	wg.Wait()
	close(out)
}

// After a long silence frames may have been lost unnoticed (the 8-bit
// counter wraps), so the next frame, even with the next counter, must not be
// applied to the old base: the receiver asks for a keyframe.
func TestVideoReceiverTreatsSilenceAsLoss(t *testing.T) {
	for len(ui.KeyframeRequests()) > 0 {
		<-ui.KeyframeRequests()
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	in := make(chan []byte, 8)
	out := make(chan *codec.TextFrame, 8)
	go func() {
		for range out {
		}
	}()
	startVideoReceiver(&wg, stop, in, out, nil)
	enc := codec.NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(10)
	img := make([]byte, 64*64*3)
	for i := range img {
		img[i] = byte(i * 3)
	}
	send := func(seq int) {
		for i := 0; i < 8*64*3; i++ {
			img[i] = byte(i*3 + seq*40)
		}
		payload, err := enc.Encode(codec.Frame{Data: img, Width: 64, Height: 64}, 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		in <- append([]byte{0, byte(seq), byte(seq)}, payload...)
	}
	send(0)
	send(1)
	time.Sleep(videoSilenceGap + 100*time.Millisecond)
	send(2) // the next counter, but after a silence
	time.Sleep(50 * time.Millisecond)
	if len(ui.KeyframeRequests()) != 1 {
		t.Fatalf("no keyframe request after %v of silence", videoSilenceGap)
	}
	<-ui.KeyframeRequests()
	close(stop)
	wg.Wait()
	close(out)
}

// A queue on the way steps the video down as loss does (colour, then
// detail, then frames), and one between clean and light holds it: no step
// up while the queue has not drained.
func TestVideoAdaptsToTheQueue(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	if l := queueVideoLoss(ms(50)); l != 0 {
		t.Fatalf("a 50 ms queue counts as %.3f loss", l)
	}
	if l := queueVideoLoss(ms(8000)); l < videoLossHeavy {
		t.Fatalf("an 8 s queue counts as %.3f loss, not heavy", l)
	}
	const ceiling, base, maxHold = 30, 10, 64
	a := videoAdapt{fps: ceiling, hold: base}
	for i := 0; i < 20; i++ {
		a.update(queueVideoLoss(ms(2000)), ceiling, ceiling, base, maxHold)
	}
	if !a.noColour || a.hold != maxHold || a.fps >= ceiling {
		t.Fatalf("after a long 2 s queue: colour off %v, hold %d, %d fps; want everything stepped down", a.noColour, a.hold, a.fps)
	}
	fps, hold := a.fps, a.hold
	for i := 0; i < 20; i++ {
		a.update(queueVideoLoss(ms(200)), fps, ceiling, base, maxHold)
	}
	if a.fps != fps || a.hold != hold || !a.noColour {
		t.Fatalf("a 200 ms queue moved the video: %d fps, hold %d, colour off %v", a.fps, a.hold, a.noColour)
	}
	for i := 0; i < 200; i++ {
		a.update(queueVideoLoss(ms(20)), a.fps, ceiling, base, maxHold)
	}
	if a.noColour || a.hold != base || a.fps != ceiling {
		t.Fatalf("a drained queue did not restore the video: colour off %v, hold %d, %d fps", a.noColour, a.hold, a.fps)
	}
}
