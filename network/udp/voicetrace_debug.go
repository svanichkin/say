package udp

// DEBUG-TEMP: the peer's voice traced frame by frame, by media index, from
// the network to the speaker. One line per frame as it starts playing (when
// it arrived and how, its decoded level, how long it waited), and a line
// for every frame that went another way: duplicate, late, made up, skipped,
// thrown away, and every time the speaker ran dry. Remove once calls are
// reliable.

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
)

type voiceRec struct {
	index     uint16
	set       bool
	rxAt      time.Time
	via       string // "voice" or "video" datagram
	size      int
	decodedAt time.Time
	level     string
	queuedMs  int // queued ahead of it when it was decoded
	madeUp    bool
	seen      time.Time // last touched
}

var vtrace struct {
	sync.Mutex
	recs [8192]voiceRec
}

// A record older than voiceRecStale is of an earlier call (each call's
// indexes start over) or of an earlier pass of the index.
const voiceRecStale = time.Minute

func vslot(index uint16) *voiceRec {
	r := &vtrace.recs[int(index/audioFrameTicks)%len(vtrace.recs)]
	if !r.set || r.index != index || time.Since(r.seen) > voiceRecStale {
		*r = voiceRec{index: index, set: true}
	}
	r.seen = time.Now()
	return r
}

// traceVoiceRx: a frame arrived from the network.
func traceVoiceRx(index uint16, via string, size int) {
	vtrace.Lock()
	r := vslot(index)
	if !r.rxAt.IsZero() {
		vtrace.Unlock()
		logs.LogV("[trace] voice #%d arrived again (%s, %d B): duplicate", index, via, size)
		return
	}
	r.rxAt, r.via, r.size = time.Now(), via, size
	vtrace.Unlock()
}

// traceVoiceDecoded: a frame was decoded for its slot (or made up for a
// missing one, madeUp).
func traceVoiceDecoded(index uint16, pcm []int16, queuedSamples int, madeUp bool) {
	level := frameLevel(pcm)
	vtrace.Lock()
	r := vslot(index)
	r.decodedAt, r.level, r.madeUp = time.Now(), level, madeUp
	r.queuedMs = queuedSamples * 1000 / audioSampleRate
	vtrace.Unlock()
}

// traceVoice logs what happened to a frame there and then.
func traceVoice(index uint16, format string, args ...any) {
	logs.LogV("[trace] voice #%d "+format, append([]any{index}, args...)...)
}

func frameLevel(pcm []int16) string {
	if len(pcm) == 0 {
		return "empty"
	}
	var acc float64
	for _, v := range pcm {
		acc += float64(v) * float64(v)
	}
	return dbfs(math.Sqrt(acc / float64(len(pcm))))
}

var voiceTraceOnce sync.Once

// startVoiceTrace joins the speaker's events with what is known of each
// frame and logs them.
func startVoiceTrace() {
	voiceTraceOnce.Do(func() {
		go func() {
			for e := range device.PlayEvents() {
				logPlayEvent(e)
				if lost := device.PlayEventsLost(); lost > 0 {
					logs.LogV("[trace] %d speaker events were lost (the log fell behind)", lost)
				}
			}
		}()
	})
}

func logPlayEvent(e device.PlayEvent) {
	ms := func(samples int) int { return samples * 1000 / audioSampleRate }
	switch e.Kind {
	case device.PlayDry:
		logs.LogV("[trace] speaker ran dry after voice #%d: %d ms of silence", e.Index, ms(e.Samples))
		return
	case device.PlayDropped:
		logs.LogV("[trace] voice #%d thrown away by the playback queue (%d ms)", e.Index, ms(e.Samples))
		return
	}
	vtrace.Lock()
	r := vtrace.recs[int(e.Index/audioFrameTicks)%len(vtrace.recs)]
	vtrace.Unlock()
	if !r.set || r.index != e.Index || e.At.Sub(r.seen) > voiceRecStale {
		logs.LogV("[trace] voice #%d playing (%d ms), nothing known of it", e.Index, ms(e.Samples))
		return
	}
	var b []byte
	b = fmt.Appendf(b, "[trace] voice #%d playing (%d ms, %s)", e.Index, ms(e.Samples), r.level)
	switch {
	case r.madeUp:
		b = append(b, ": made up for a missing frame"...)
	case r.rxAt.IsZero():
		b = append(b, ": never arrived"...)
	default:
		b = fmt.Appendf(b, ": arrived %s by %s datagram, %d B", r.rxAt.Format("15:04:05.000"), r.via, r.size)
	}
	if !r.decodedAt.IsZero() {
		b = fmt.Appendf(b, "; %d ms queued ahead of it", r.queuedMs)
	}
	if !r.rxAt.IsZero() && !r.madeUp {
		b = fmt.Appendf(b, "; %d ms from arrival to the speaker", e.At.Sub(r.rxAt).Milliseconds())
	}
	logs.LogV("%s", b)
}

func speechOrSilence(encodedBytes int) string {
	switch {
	case encodedBytes == 0:
		return "nothing: silence not sent"
	case encodedBytes <= audioSIDMaxBytes:
		return "silence"
	}
	return "speech"
}
