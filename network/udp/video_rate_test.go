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
				in <- lowPacket(index, byte(seq), payload)
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
			in <- lowPacket(index, byte(seq), payload)
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
	recoveryRequests()
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
		in <- lowPacket(uint16(seq), byte(seq), payload)
	}
	requests := func() int {
		time.Sleep(50 * time.Millisecond)
		return recoveryRequests()
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

// lowPacket is a video datagram's part after the media header, as the
// video receiver gets it: the media index, the datagram counter, the frame
// as the one band's (its own counter the same).
func lowPacket(index uint16, seq byte, frame []byte) []byte {
	videoBandRows = 0 // one band: the whole frame
	pkt := []byte{byte(index >> 8), byte(index), seq}
	pkt = appendLayerHead(pkt, layerHead{gen: 1, rows: 8, bands: 1, seq: seq})
	return append(pkt, frame...)
}

// recoveryRequests counts (and takes) the keyframe and recovery requests
// waiting for the control connection.
func recoveryRequests() int {
	n := 0
	for len(ui.VideoCtlOut()) > 0 {
		if msg := <-ui.VideoCtlOut(); len(msg) > 0 && (msg[0] == ctlKeyframe || msg[0] == ctlRecover) {
			n++
		}
	}
	return n
}

// After a long silence frames may have been lost unnoticed (the 8-bit
// counter wraps), so the next frame, even with the next counter, must not be
// applied to the old base: the receiver asks for a keyframe.
func TestVideoReceiverTreatsSilenceAsLoss(t *testing.T) {
	recoveryRequests()
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
		in <- lowPacket(uint16(seq), byte(seq), payload)
	}
	send(0)
	send(1)
	time.Sleep(videoLayerSilence + 100*time.Millisecond)
	send(2) // the next counter, but after a silence
	time.Sleep(50 * time.Millisecond)
	if recoveryRequests() != 1 {
		t.Fatalf("no keyframe request after %v of silence", videoLayerSilence)
	}
	close(stop)
	wg.Wait()
	close(out)
}
