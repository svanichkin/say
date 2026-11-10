package udp

import (
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/svanichkin/say/device"
)

type countedDecoder struct{ decoded, concealed int }

func (d *countedDecoder) DecodeFrame(_ []byte, dst []int16) (int, error) {
	d.decoded++
	for i := range dst {
		dst[i] = 1000
	}
	return len(dst), nil
}
func (d *countedDecoder) Conceal(dst []int16) (int, error) {
	d.concealed++
	clear(dst)
	return len(dst), nil
}
func (*countedDecoder) IsDTXFrame([]byte) bool { return false }

func TestDecodedDuplicatesDoNotChangeDelayOrDecoder(t *testing.T) {
	dec := &countedDecoder{}
	q := device.NewPlaybackQueue(128)
	p := newAudioPlayout(dec, 160, q)
	now := time.Now()
	for i := 0; i < 8; i++ {
		p.onPacket(uint16(i*5), []byte{1}, now)
	}
	lead := p.lead
	for i := 0; i < 100; i++ {
		p.onPacket(0, []byte{1}, now)
		p.onTickAt(now)
	}
	if p.lead != lead || dec.concealed != 0 || dec.decoded != 8 || p.lateEvents != 0 {
		t.Fatalf("duplicates changed playback: lead=%d, decodes=%d, PLC=%d, late=%d", p.lead, dec.decoded, dec.concealed, p.lateEvents)
	}
}

func TestOneLateSlotCountsOnceAndKeepsTheLead(t *testing.T) {
	q := device.NewPlaybackQueue(128)
	p := newAudioPlayout(&countedDecoder{}, 160, q)
	now := time.Now()
	for i := 0; i < 8; i++ {
		p.onPacket(uint16(i*5), []byte{1}, now)
	}
	index := p.nextIndex
	p.conceal()
	before := p.lead
	for i := 0; i < 20; i++ {
		p.onPacket(index, []byte{1}, now)
	}
	if p.lead != before || p.lateEvents != 1 {
		t.Fatalf("lead=%d late=%d", p.lead, p.lateEvents)
	}
}

func TestPCMRateCorrectionPreservesFrameJoins(t *testing.T) {
	pcm := make([]int16, 160)
	for i := range pcm {
		pcm[i] = int16(10000 * math.Sin(float64(i)*2*math.Pi*337/8000))
	}
	for _, n := range []int{152, 159, 161, 168} {
		out := resizePCM(pcm, n)
		if out[0] != pcm[0] || out[n-1] != pcm[159] {
			t.Fatalf("%d samples: changed frame boundary", n)
		}
		for _, v := range out {
			if v > 10000 || v < -10000 {
				t.Fatal("resampler overshoot")
			}
		}
	}
}

type senderTestConn struct {
	net.PacketConn
	packets chan []byte
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *senderTestConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if c.release != nil {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	c.packets <- append([]byte(nil), p...)
	return len(p), nil
}

type senderTestEncoder struct{ frames chan []int16 }

func (e senderTestEncoder) EncodeFrame(p []int16, out []byte) (int, error) {
	if e.frames != nil {
		e.frames <- append([]int16(nil), p...)
	}
	out[0] = 1
	return 1, nil
}
func testPeer() *net.UDPAddr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234} }

// frameIndexes returns the media index of every frame of a voice datagram.
func frameIndexes(t *testing.T, packet []byte) []uint16 {
	t.Helper()
	_, first, ok := decodeMediaHeader(packet)
	if !ok {
		t.Fatalf("bad media header")
	}
	frames, err := splitAudioBundle(packet[mediaHeaderBytes:])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uint16, len(frames))
	for k := range frames {
		out[k] = advanceMediaIndex(first, int32(k)*audioFrameTicks)
	}
	return out
}

func TestSenderPreservesCaptureGapAndIgnoresCallbackJitter(t *testing.T) {
	for _, gap := range []bool{false, true} {
		t.Run(fmt.Sprint(gap), func(t *testing.T) {
			conn := &senderTestConn{packets: make(chan []byte, 16)}
			mic := make(chan device.AudioCaptureFrame, 8)
			stop := make(chan struct{})
			defer close(stop)
			var wg sync.WaitGroup
			epoch := time.Now()
			startAudioSender(&wg, conn, nil, stop, testPeer, senderTestEncoder{}, nil, 160, epoch, mic, nil)
			for i := 0; i < 4; i++ {
				sample := uint64(i * 160)
				at := epoch.Add(time.Duration(i*20) * time.Millisecond)
				if gap && i >= 2 {
					sample += 160
					at = at.Add(20 * time.Millisecond)
				}
				if !gap && i%2 == 1 {
					at = at.Add(7 * time.Millisecond)
				}
				mic <- device.AudioCaptureFrame{Samples: make([]int16, 160), FirstSample: sample, CapturedAt: at}
			}
			close(mic)
			wg.Wait()
			close(conn.packets)
			i := 0
			for packet := range conn.packets {
				for _, index := range frameIndexes(t, packet) {
					want := i * 5
					if gap && i >= 2 {
						want += 5
					}
					if int(index) != want {
						t.Fatalf("frame %d index %d, want %d", i, index, want)
					}
					i++
				}
			}
			if i != 4 {
				t.Fatalf("sent %d frames", i)
			}
		})
	}
}

func TestBlockedTransportDoesNotBlockCaptureProcessing(t *testing.T) {
	conn := &senderTestConn{packets: make(chan []byte, 256), entered: make(chan struct{}), release: make(chan struct{})}
	defer closeOnce(conn.release)()
	mic := make(chan device.AudioCaptureFrame, 256)
	stop := make(chan struct{})
	defer close(stop)
	var wg sync.WaitGroup
	epoch := time.Now()
	encoded := make(chan []int16, 256)
	startAudioSender(&wg, conn, nil, stop, testPeer, senderTestEncoder{encoded}, nil, 160, epoch, mic, nil)
	mic <- device.AudioCaptureFrame{Samples: make([]int16, 160), CapturedAt: epoch}
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("writer not entered")
	}
	for i := 1; i < 200; i++ {
		mic <- device.AudioCaptureFrame{Samples: make([]int16, 160), FirstSample: uint64(i * 160), CapturedAt: epoch.Add(time.Duration(i*20) * time.Millisecond)}
	}
	for i := 0; i < 200; i++ {
		select {
		case <-encoded:
		case <-time.After(time.Second):
			t.Fatal("blocked WriteTo stalled microphone processing")
		}
	}
	close(mic)
	close(conn.release)
	wg.Wait()
	close(conn.packets)
	var indexes []uint16
	for packet := range conn.packets {
		indexes = append(indexes, frameIndexes(t, packet)...)
	}
	// The final EncodeFrame notification can precede its enqueue, so one
	// additional packet may enter after the writer is released.
	if len(indexes) > 2+audioSendQueueFrames || indexes[len(indexes)-1] != 199*5 {
		t.Fatalf("unbounded/stale transmit queue: %v", indexes)
	}
}

func closeOnce(ch chan struct{}) func() {
	return func() {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func TestPlayoutCompensatesIndependentAudioClocks(t *testing.T) {
	for _, ppm := range []int{-1000, 1000} {
		t.Run(fmt.Sprint(ppm), func(t *testing.T) {
			const durationMS = 180000
			dec := &countedDecoder{}
			q := device.NewPlaybackQueue(128)
			p := newAudioPlayout(dec, 160, q)
			start := time.Now()
			nextPlayback := -1.0
			period := 20.0 / (1 + float64(ppm)/1e6)
			carry := 0
			underruns := 0
			maxQueue := 0
			for ms := 0; ms < durationMS; ms++ {
				now := start.Add(time.Duration(ms) * time.Millisecond)
				if ms%20 == 0 {
					p.onPacket(uint16((ms/20*5)&int(mediaIndexMask)), []byte{1}, now)
				}
				if ms%5 == 0 {
					p.onTickAt(now)
				}
				if p.ready && nextPlayback < 0 {
					nextPlayback = float64(ms)
				}
				if p.ready && float64(ms) >= nextPlayback {
					for carry < 160 {
						f, ok := q.TakeFrame()
						if !ok {
							underruns++
							break
						}
						carry += len(f.Samples)
					}
					carry = max(0, carry-160)
					nextPlayback += period
				}
				maxQueue = max(maxQueue, q.QueuedSamples())
			}
			t.Logf("clock drift %d ppm: PLC=%d underruns=%d max queue=%d ms final=%d ms", ppm, dec.concealed, underruns, maxQueue/8, q.QueuedSamples()/8)
			if underruns != 0 || dec.concealed != 0 || maxQueue > 12*160 {
				t.Fatal("unbounded clock drift or lost speech")
			}
		})
	}
}
