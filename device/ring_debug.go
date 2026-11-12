//go:build !plan9

package device

// DEBUG-TEMP: an audio ring in the speaker's numbers (see
// playback_debug.go): what the device asked for and played, how often it ran
// dry, and microphone samples lost to a full ring. Remove once calls are
// reliable.

import (
	"time"

	"github.com/svanichkin/say/logs"
)

// noteWritten adds samples written for the unit to the level played.
func (s *playStats) noteWritten(samples []int16) {
	s.played += len(samples)
	for _, v := range samples {
		x := int(v)
		if x < 0 {
			x = -x
		}
		s.peak = max(s.peak, x)
		s.sumSq += float64(x) * float64(x)
	}
}

// noteStats reports the unit's counters once a second, and every time it
// ran dry.
func (r *ringRenderer) noteStats(st RingState) {
	prev := r.lastState
	r.lastState = st
	if d := st.DrySamples - prev.DrySamples; d > 0 && prev.Callbacks > 0 {
		tracePlay(PlayEvent{Kind: PlayDry, Index: r.lastIdx, At: time.Now(), Samples: int(d)})
	}
	if d := st.MicDropped - prev.MicDropped; d > 0 {
		logs.LogV("[debug] audio ring: %d microphone samples lost, it was full (Go held up)", d)
	}
	now := time.Now()
	if r.statsSince.IsZero() {
		r.statsSince = now
		r.stats = playStats{since: now}
		return
	}
	if now.Sub(r.statsSince) < time.Second {
		return
	}
	s := r.stats
	s.callbacks = int(st.Callbacks - r.statsAt.Callbacks)
	s.asked = int(st.Asked - r.statsAt.Asked)
	s.underruns = int(st.DryCallbacks - r.statsAt.DryCallbacks)
	s.silent = int(st.DrySamples - r.statsAt.DrySamples)
	select {
	case playStatsOut <- s:
	default:
	}
	r.statsAt = st
	r.statsSince = now
	r.stats = playStats{since: now}
}
