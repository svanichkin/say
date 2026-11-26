//go:build !plan9

package device

import (
	"log"
	"sync"
	"time"

	malgo "github.com/gen2brain/malgo"
)

// The tones (a call ringing, the fail sound) play on a device of their own,
// apart from a call's sound, opened once when Say starts (WarmTones) and
// kept running, silent between tones: Core Audio took seconds to open a
// device and seconds more to start asking for sound on a busy Mac, and a
// ring is wanted at once. It is served as a call's speaker is: its audio
// thread runs only C, from a ring (ring.go) a goroutine keeps a little
// ahead (toneAhead), so a tone stopped stops at once.

// toneAhead is how far ahead of the device the tones are queued.
const toneAhead = 80 * time.Millisecond

// toneRise is how long a ring takes to reach its full level, from
// toneQuiet of it.
const (
	toneRise  = 8 * time.Second
	toneQuiet = 0.08
)

// toneGain is the level of sample i of a ring rising over rise samples:
// from toneQuiet up, in steps the ear hears as even (squared).
func toneGain(i, rise int) float64 {
	if i >= rise {
		return 1
	}
	k := float64(i) / float64(rise)
	return toneQuiet + (1-toneQuiet)*k*k
}

// toneTrack is a tone being played.
type toneTrack struct {
	pcm   []int16
	loop  bool
	pos   int // the next sample to queue
	total int // samples queued so far
}

var tonePlayer struct {
	once  sync.Once
	mu    sync.Mutex
	rate  int
	track *toneTrack
}

// WarmTones opens the tones' device aside, at rate (the tones' own); a
// call's ring then starts at once.
func WarmTones(rate int) {
	tonePlayer.once.Do(func() {
		tonePlayer.rate = rate
		go runTones(rate)
	})
}

// PlayTone plays pcm (mono 16-bit at the rate WarmTones was given): round
// and round when loop (a call ringing, rising from quiet), else once (the
// fail sound). It replaces a tone playing. stop ends it at once; it may be
// called again.
func PlayTone(pcm []int16, rate int, loop bool) (stop func()) {
	WarmTones(rate)
	t := &toneTrack{pcm: pcm, loop: loop}
	tonePlayer.mu.Lock()
	if tonePlayer.rate != rate {
		log.Printf("[audio] tone at %d Hz on a device at %d Hz", rate, tonePlayer.rate)
	}
	tonePlayer.track = t
	tonePlayer.mu.Unlock()
	return func() {
		tonePlayer.mu.Lock()
		if tonePlayer.track == t {
			tonePlayer.track = nil
		}
		tonePlayer.mu.Unlock()
	}
}

// runTones opens the device and keeps it fed for as long as Say runs.
func runTones(rate int) {
	callStreams()
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Printf("[malgo] %s", message)
	})
	if err != nil {
		log.Printf("[audio] tones: %v", err)
		return
	}
	ring, err := newNativeRing(rate, 1)
	if err != nil {
		log.Printf("[audio] tones: %v", err)
		return
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = 1
	cfg.SampleRate = uint32(rate)
	dev, err := malgo.InitDeviceRaw(ctx.Context, cfg, deviceDataProc(), ring.ptr(), nil)
	if err != nil {
		log.Printf("[audio] tones: %v", err)
		return
	}
	if err := dev.Start(); err != nil {
		log.Printf("[audio] tones: %v", err)
		return
	}
	ahead := int(toneAhead.Seconds() * float64(rate))
	rise := int(toneRise.Seconds() * float64(rate))
	buf := make([]int16, ahead)
	tick := time.NewTicker(ringPoll)
	defer tick.Stop()
	for range tick.C {
		tonePlayer.mu.Lock()
		t := tonePlayer.track
		tonePlayer.mu.Unlock()
		if t == nil {
			continue // the device plays silence
		}
		st := ring.State()
		room := ahead - int(st.Written-st.Played)
		n := 0
		for n < room {
			if t.pos >= len(t.pcm) {
				if !t.loop {
					break
				}
				t.pos = 0
			}
			v := float64(t.pcm[t.pos])
			if t.loop && t.total < rise {
				v *= toneGain(t.total, rise)
			}
			buf[n] = int16(v)
			n++
			t.pos++
			t.total++
		}
		if n > 0 {
			ring.Write(buf[:n])
		}
		if !t.loop && t.pos >= len(t.pcm) {
			tonePlayer.mu.Lock()
			if tonePlayer.track == t {
				tonePlayer.track = nil // all queued: played out within toneAhead
			}
			tonePlayer.mu.Unlock()
		}
	}
}
