package udp

import (
	"bytes"
	"math/rand"
	"testing"
	"time"
)

func TestAudioBundleRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 500; trial++ {
		frames := make([][]byte, 1+rng.Intn(audioBundleMax))
		for k := range frames {
			f := make([]byte, 1+rng.Intn(300))
			rng.Read(f)
			frames[k] = f
		}
		got, err := splitAudioBundle(appendAudioBundle(nil, frames))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(frames) {
			t.Fatalf("%d frames back, sent %d", len(got), len(frames))
		}
		for k := range frames {
			if !bytes.Equal(got[k], frames[k]) {
				t.Fatalf("trial %d frame %d differs", trial, k)
			}
		}
	}
	// Garbage is refused, never a panic.
	for trial := 0; trial < 2000; trial++ {
		b := make([]byte, rng.Intn(20))
		rng.Read(b)
		_, _ = splitAudioBundle(b)
	}
	for _, bad := range [][]byte{nil, {1}, {0, 5}, {9, 1, 2}, {2, 5, 1, 2}, {2, 0, 1}} {
		if _, err := splitAudioBundle(bad); err == nil {
			t.Fatalf("% x accepted", bad)
		}
	}
}

func TestAudioBundlerPairsConsecutiveFrames(t *testing.T) {
	var b audioBundler
	now := time.Now()
	frame := func(i int) []byte { return []byte{byte(i)} }
	var sent [][]byte
	for i := 0; i < 4; i++ { // 4 consecutive frames: 2 datagrams
		sent = append(sent, b.add(uint16(i*audioFrameTicks), frame(i), now)...)
	}
	if len(sent) != 2 {
		t.Fatalf("%d datagrams for 4 frames", len(sent))
	}
	_, first, _ := decodeMediaHeader(sent[1])
	if first != 2*audioFrameTicks {
		t.Fatalf("second datagram starts at %d", first)
	}
	// A gap (DTX) sends what is pending at once.
	sent = b.add(10*audioFrameTicks, frame(10), now)
	if len(sent) != 0 || b.due(now) != nil {
		t.Fatal("a single frame went out before its wait")
	}
	sent = b.add(20*audioFrameTicks, frame(20), now)
	if len(sent) != 1 {
		t.Fatalf("gap: %d datagrams, want the pending one", len(sent))
	}
	frames, _ := splitAudioBundle(sent[0][mediaHeaderBytes:])
	if len(frames) != 1 || frames[0][0] != 10 {
		t.Fatalf("gap flushed %v", frames)
	}
	// A lone frame (DTX update) waits at most audioBundleWait.
	if b.due(now.Add(audioBundleWait-time.Millisecond)) != nil {
		t.Fatal("lone frame sent early")
	}
	if b.due(now.Add(audioBundleWait)) == nil {
		t.Fatal("lone frame held past its wait")
	}
}

// A lost datagram loses all its frames; the playout conceals exactly those
// and goes on with the right media indices.
func TestPlayoutWithBundledFrames(t *testing.T) {
	const n = 40
	arrivals := map[int][]uint16{}
	for i := 0; i < n; i += audioBundleFrames {
		if i == 20 { // the datagram with frames 20 and 21 is lost
			continue
		}
		last := i + audioBundleFrames - 1
		for k := i; k <= last; k++ {
			arrivals[last] = append(arrivals[last], uint16(k*audioFrameTicks))
		}
	}
	played := simulate(t, n+10, arrivals, nil)
	if u := underruns(played, 12); u != 0 {
		t.Fatalf("%d underruns: %v", u, played)
	}
	end := 0 // after the last real frame the playout conceals the end of the stream
	for i, v := range played {
		if v >= 1000 {
			end = i
		}
	}
	concealed := 0
	for _, v := range played[:end] {
		if v == -1 {
			concealed++
		}
	}
	if concealed != audioBundleFrames {
		t.Fatalf("%d frames concealed, want %d: %v", concealed, audioBundleFrames, played)
	}
	for k := 1000 + 22; k < 1000+30; k++ { // frames after the loss play in order
		found := false
		for _, v := range played {
			if int(v) == k {
				found = true
			}
		}
		if !found {
			t.Fatalf("frame %d never played: %v", k-1000, played)
		}
	}
}
