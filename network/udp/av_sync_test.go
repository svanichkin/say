package udp

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/device"
)

// echoDecoder returns the payload's first byte as PCM so tests can follow frames.
type echoDecoder struct{}

func (echoDecoder) DecodeFrame(p []byte, dst []int16) (int, error) {
	for i := range dst {
		dst[i] = int16(p[0]) + 1
	}
	return len(dst), nil
}
func (echoDecoder) Conceal(dst []int16) (int, error) { clear(dst); return len(dst), nil }
func (echoDecoder) IsDTXFrame([]byte) bool          { return false }

// The receiver must keep playback latency near its jitter buffer lead while
// packets arrive in real time with jitter.
func TestAudioLatencyStaysBounded(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	in := make(chan []byte, audioBufferMaxFrames)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	startAudioReceiver(&wg, echoDecoder{}, frame, stop, in, q)

	go func() {
		for i := 0; i < 150; i++ {
			idx := uint16(i * audioFrameTicks)
			in <- []byte{byte(idx >> 8), byte(idx), byte(i), 0}
			// 20 ms on average, with up to 15 ms of jitter.
			time.Sleep(time.Duration(10+i%3*5) * time.Millisecond)
			if i%3 == 2 {
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()
	tk := time.NewTicker(20 * time.Millisecond)
	defer tk.Stop()
	var worst int
	for n := 0; n < 150; n++ {
		<-tk.C
		if n > 20 {
			worst = max(worst, q.QueuedSamples())
		}
		q.TakeFrame()
	}
	close(stop)
	wg.Wait()
	worstMs := worst / 8
	t.Logf("worst playback queue: %d ms", worstMs)
	if worstMs > audioQueueMaxFrames*audioFrameMillis {
		t.Fatalf("audio latency grew to %d ms", worstMs)
	}
}

// Video frames are shown when the audio playback clock reaches their
// timestamp, also when audio runs well behind video arrival.
func TestVideoFollowsAudioClock(t *testing.T) {
	enc := codec.NewEncoder(false, 0, 25, false)
	raw := codec.Frame{Data: make([]byte, 32*32*3), Width: 32, Height: 32}
	var clock atomic.Int32 // audio playback index
	playback := func() (uint16, bool) { return uint16(clock.Load()) & mediaIndexMask, true }

	in := make(chan []byte, 64)
	out := make(chan *codec.TextFrame, 64)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	startVideoReceiver(&wg, stop, in, out, playback)

	const lagTicks = 30 // audio plays 120 ms behind video arrival
	shown := 0
	for i := 0; i < 50; i++ { // 25 fps for 2 s
		idx := uint16(i * 10) // 40 ms per frame on the 4 ms timeline
		data, err := enc.Encode(raw, 4, 2)
		if err != nil {
			t.Fatal(err)
		}
		in <- append([]byte{byte(idx >> 8), byte(idx)}, data...)
		clock.Store(int32(idx) - lagTicks)
		time.Sleep(40 * time.Millisecond)
		for len(out) > 0 {
			<-out
			shown++
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("shown %d of 50 frames with audio %d ms behind", shown, lagTicks*4)
	if shown < 40 {
		t.Fatalf("only %d of 50 video frames shown while following the audio clock", shown)
	}
}
