package udp

import (
	"bytes"
	"math/rand"
	"net"
	"sync"
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
	at := func(i int) time.Time { return now.Add(time.Duration(i*audioFrameMillis) * time.Millisecond) }
	var sent [][]byte
	for i := 0; i < 4; i++ { // consecutive frames: a full pair goes out at once
		sent = append(sent, b.add(uint16(i*audioFrameTicks), frame(i), at(i))...)
		if want := (i + 1) / 2; len(sent) != want {
			t.Fatalf("after frame %d: %d datagrams, want %d", i, len(sent), want)
		}
	}
	for k, d := range sent {
		_, first, _ := decodeMediaHeader(d)
		frames, _ := splitAudioBundle(d[mediaHeaderBytes:])
		if first != uint16(2*k*audioFrameTicks) || len(frames) != 2 {
			t.Fatalf("datagram %d: first %d, %d frames", k, first, len(frames))
		}
	}
	// A gap (DTX) sends what is pending at once.
	b.add(10*audioFrameTicks, frame(10), at(10))
	sent = b.add(20*audioFrameTicks, frame(20), at(20))
	if len(sent) != 1 {
		t.Fatalf("gap: %d datagrams, want the pending one", len(sent))
	}
	frames, _ := splitAudioBundle(sent[0][mediaHeaderBytes:])
	if len(frames) != 1 || frames[0][0] != 10 {
		t.Fatalf("gap flushed %v", frames)
	}
	// A lone frame (DTX update) waits at most audioBundleWait.
	if b.due(at(20).Add(audioBundleWait-time.Millisecond)) != nil {
		t.Fatal("lone frame sent early")
	}
	if b.due(at(20).Add(audioBundleWait)) == nil {
		t.Fatal("lone frame held past its wait")
	}
	// A video datagram takes the waiting frame: nothing is left to send.
	b.add(30*audioFrameTicks, frame(30), at(30))
	if first, frames, ok := b.takeFrames(); !ok || first != 30*audioFrameTicks || len(frames) != 1 {
		t.Fatalf("takeFrames: %d %v %v", first, frames, ok)
	}
	if b.due(at(40)) != nil {
		t.Fatal("frames taken by video were sent again")
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

func TestVideoHeadRoundTrip(t *testing.T) {
	video := []byte("TEXT\x04video payload")
	for _, voice := range [][][]byte{nil, {{1, 2, 3}}, {{4}, {5, 6}}} {
		p := appendVideoHead(nil, 77, 0x1234, voice)
		p = append(p, video...)
		seq, first, gotVoice, gotVideo, err := splitVideoHead(p)
		if err != nil {
			t.Fatal(err)
		}
		if seq != 77 || !bytes.Equal(gotVideo, video) || len(gotVoice) != len(voice) {
			t.Fatalf("seq %d video %q voice %v", seq, gotVideo, gotVoice)
		}
		if len(voice) > 0 && first != 0x1234 {
			t.Fatalf("voice index %#x", first)
		}
		for k := range voice {
			if !bytes.Equal(gotVoice[k], voice[k]) {
				t.Fatalf("voice frame %d differs", k)
			}
		}
	}
	rng := rand.New(rand.NewSource(5))
	for trial := 0; trial < 3000; trial++ {
		b := make([]byte, rng.Intn(24))
		rng.Read(b)
		_, _, _, _, _ = splitVideoHead(b) // no panic
	}
	for _, bad := range [][]byte{nil, {1}, {1, 2}, {1, 1}, {1, 1, 0, 0, 9, 1}} {
		if _, _, _, _, err := splitVideoHead(bad); err == nil {
			t.Fatalf("% x accepted", bad)
		}
	}
}

// Voice frames riding in a video datagram reach the voice decoder with their
// media indices; the video decoder gets the frame without them.
func TestReceiverSplitsVoiceFromVideo(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	audioIn := make(chan []byte, 16)
	videoIn := make(chan []byte, 16)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var peer *net.UDPAddr
	var mu sync.Mutex
	startReceiver(&wg, pc, stop, audioIn, videoIn,
		func() *net.UDPAddr { mu.Lock(); defer mu.Unlock(); return peer },
		func(a *net.UDPAddr) { mu.Lock(); peer = a; mu.Unlock() })
	defer func() { close(stop); pc.Close(); wg.Wait() }()
	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	header := encodeMediaHeader(mediaKindVideo, 500)
	dg := append(header[:], appendVideoHead(nil, 9, 300, [][]byte{{0xa1, 0xa2}, {0xb1}})...)
	dg = append(dg, []byte("frame")...)
	if _, err := conn.Write(dg); err != nil {
		t.Fatal(err)
	}
	for k, want := range []struct {
		index uint16
		data  []byte
	}{{300, []byte{0xa1, 0xa2}}, {300 + audioFrameTicks, []byte{0xb1}}} {
		select {
		case pkt := <-audioIn:
			if got := uint16(pkt[0])<<8 | uint16(pkt[1]); got != want.index || !bytes.Equal(pkt[2:], want.data) {
				t.Fatalf("voice frame %d: index %d data % x", k, got, pkt[2:])
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("voice frame %d not delivered", k)
		}
	}
	select {
	case pkt := <-videoIn:
		if got := uint16(pkt[0])<<8 | uint16(pkt[1]); got != 500 || pkt[2] != 9 || string(pkt[3:]) != "frame" {
			t.Fatalf("video pkt index %d seq %d payload %q", got, pkt[2], pkt[3:])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("video frame not delivered")
	}
	// A video datagram without voice delivers video only.
	dg = append(header[:], appendVideoHead(nil, 10, 0, nil)...)
	dg = append(dg, []byte("next")...)
	conn.Write(dg)
	select {
	case pkt := <-videoIn:
		if pkt[2] != 10 || string(pkt[3:]) != "next" {
			t.Fatalf("plain video pkt seq %d payload %q", pkt[2], pkt[3:])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plain video frame not delivered")
	}
	if len(audioIn) != 0 {
		t.Fatal("voice out of a datagram that carried none")
	}
}
