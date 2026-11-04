package udp

import (
	"testing"
	"time"

	"github.com/svanichkin/say/device"
)

// fakeDecoder marks decoded samples with 1000+payload[0] and concealed ones with -1.
type fakeDecoder struct{ frame int }

func (d fakeDecoder) DecodeFrame(p []byte, dst []int16) (int, error) {
	for i := range dst[:d.frame] {
		dst[i] = 1000 + int16(p[0])
	}
	return d.frame, nil
}
func (d fakeDecoder) Conceal(dst []int16) (int, error) {
	for i := range dst[:d.frame] {
		dst[i] = -1
	}
	return d.frame, nil
}
func (fakeDecoder) IsDTXFrame(p []byte) bool { return len(p) > 1 && p[1] == 'S' }

// simulate plays 20 ms of audio per step: the network delivers the packets
// scheduled for that step, the receiver ticks, and the speaker takes one
// frame. It returns what the speaker played (0 = underrun).
func simulate(t *testing.T, steps int, arrivals map[int][]uint16, sid map[uint16]bool) []int16 {
	t.Helper()
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	var played []int16
	for step := 0; step < steps; step++ {
		for _, idx := range arrivals[step] {
			payload := []byte{byte(idx / audioFrameTicks), 0}
			if sid[idx] {
				payload[1] = 'S'
			}
			p.onPacket(idx, payload, now)
		}
		p.onTick()
		if f, ok := q.TakeFrame(); ok {
			played = append(played, f.Samples[0])
		} else {
			played = append(played, 0)
		}
		now = now.Add(20 * time.Millisecond)
	}
	return played
}

func underruns(played []int16, from int) int {
	n := 0
	for _, v := range played[from:] {
		if v == 0 {
			n++
		}
	}
	return n
}

func TestPlayoutConcealsLatePacketsInsteadOfUnderrun(t *testing.T) {
	arrivals := map[int][]uint16{}
	for i := 0; i < 50; i++ {
		step := i
		if i >= 20 && i < 26 {
			step = 26 // a 120 ms jitter spike delays six packets, then they burst in
		}
		arrivals[step] = append(arrivals[step], uint16(i*audioFrameTicks))
	}
	played := simulate(t, 60, arrivals, nil)
	// After priming (4 frames lead), the speaker must never starve while the peer talks.
	if n := underruns(played[:50], audioBufferLeadFrames+1); n != 0 {
		t.Fatalf("speaker underran %d times during a jitter spike: %v", n, played)
	}
}

func TestPlayoutFillsDTXSilenceWithComfortNoise(t *testing.T) {
	arrivals := map[int][]uint16{}
	sid := map[uint16]bool{}
	for i := 0; i < 10; i++ { // speech
		arrivals[i] = append(arrivals[i], uint16(i*audioFrameTicks))
	}
	for i := 10; i < 90; i += audioDTXUpdateFrames { // DTX: one SID every 8 frames
		idx := uint16(i * audioFrameTicks)
		sid[idx] = true
		arrivals[i] = append(arrivals[i], idx)
	}
	for i := 90; i < 100; i++ { // speech again
		arrivals[i] = append(arrivals[i], uint16(i*audioFrameTicks))
	}
	played := simulate(t, 105, arrivals, sid)
	if n := underruns(played[:100], audioBufferLeadFrames+1); n != 0 {
		t.Fatalf("speaker underran %d times across DTX silence: %v", n, played)
	}
	sids := 0
	for _, v := range played {
		if v >= 1000+10 && v < 1000+90 && (int(v)-1000-10)%audioDTXUpdateFrames == 0 {
			sids++
		}
	}
	if sids != 10 {
		t.Fatalf("played %d of 10 SID frames; SIDs must not be dropped as late: %v", sids, played)
	}
}

// playedCounts drains the queue and counts decoded (>= 1000) and concealed (-1) frames.
func playedCounts(q *device.PlaybackQueue) (decoded, concealed int, order []int16) {
	for {
		f, ok := q.TakeFrame()
		if !ok {
			return
		}
		if f.Samples[0] == -1 {
			concealed++
		} else {
			decoded++
		}
		order = append(order, f.Samples[0])
	}
}

func TestPlayoutToleratesTimestampJitter(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(64)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	var decoded, concealed int
	for i := 0; i < 50; i++ {
		idx := uint16(i * audioFrameTicks)
		if i%2 == 1 {
			idx-- // every second block timestamped one 4 ms tick early
		}
		p.onPacket(idx, []byte{byte(i), 0}, now)
		if i >= audioBufferLeadFrames { // the speaker plays one frame per packet
			if f, ok := q.TakeFrame(); ok {
				if f.Samples[0] == -1 {
					concealed++
				} else {
					decoded++
				}
			}
		}
		now = now.Add(20 * time.Millisecond)
	}
	d, c, _ := playedCounts(q)
	decoded, concealed = decoded+d, concealed+c
	if decoded != 50 || concealed != 0 {
		t.Fatalf("decoded %d, concealed %d of 50 jittered packets; want all 50 decoded", decoded, concealed)
	}
}

func TestPlayoutReordersPacketsBeforeTheirSlotIsDue(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(64)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	for _, i := range []int{0, 2, 1, 3, 4, 6, 5, 7, 8, 9} {
		p.onPacket(uint16(i*audioFrameTicks), []byte{byte(i), 0}, now)
	}
	decoded, concealed, order := playedCounts(q)
	if decoded != 10 || concealed != 0 {
		t.Fatalf("decoded %d, concealed %d of 10 reordered packets: %v", decoded, concealed, order)
	}
	for i, v := range order {
		if v != int16(1000+i) {
			t.Fatalf("played out of order: %v", order)
		}
	}
}
