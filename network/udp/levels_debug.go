package udp

// DEBUG-TEMP: sound levels, logged once a second, to tell a voice that was
// never captured (or was cut by echo cancellation) from one lost on the way
// or not played. Remove once calls are reliable.

import (
	"math"
	"strconv"
	"time"

	"github.com/svanichkin/say/logs"
)

// audioSIDMaxBytes: an encoded frame this small is silence (an AMR SID
// frame, an Opus DTX frame), not speech.
const audioSIDMaxBytes = 8

type levelMeter struct {
	name      string
	sumSq     float64
	samples   int
	peak      int
	frames    int
	speech    int // frames sent as speech (not silence) / decoded
	concealed int // frames made up for missing ones (receiving)
	since     time.Time
}

// add counts a frame: its samples, and whether it went as speech (sending)
// or was decoded (receiving).
func (m *levelMeter) add(pcm []int16, speech bool) {
	for _, v := range pcm {
		x := int(v)
		if x < 0 {
			x = -x
		}
		m.peak = max(m.peak, x)
		m.sumSq += float64(x) * float64(x)
	}
	m.samples += len(pcm)
	m.frames++
	if speech {
		m.speech++
	}
	m.flush()
}

// conceal counts a frame made up for a missing one.
func (m *levelMeter) conceal() {
	m.frames++
	m.concealed++
	m.flush()
}

func (m *levelMeter) flush() {
	now := time.Now()
	if m.since.IsZero() {
		m.since = now
		return
	}
	if now.Sub(m.since) < time.Second {
		return
	}
	rms := 0.0
	if m.samples > 0 {
		rms = math.Sqrt(m.sumSq / float64(m.samples))
	}
	logs.LogV("[debug] %s: level %s avg, %s peak; %d frames: %d speech, %d concealed",
		m.name, dbfs(rms), dbfs(float64(m.peak)), m.frames, m.speech, m.concealed)
	*m = levelMeter{name: m.name, since: now}
}

func dbfs(v float64) string {
	if v < 1 {
		return "silent"
	}
	return formatDB(20 * math.Log10(v/32768))
}

func formatDB(db float64) string {
	return strconv.Itoa(int(math.Round(db))) + " dBFS"
}
