package device

// DEBUG-TEMP: what the speaker actually gets, logged once a second: how much
// it asked for, how much was sound, how often and for how long the queue ran
// dry, the level played, and the frames the queue threw away. The audio
// callback only adds numbers; a goroutine logs them. Remove once calls are
// reliable.

import (
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/logs"
)

type playStats struct {
	callbacks, asked, played int
	underruns, silent        int // samples of silence after the queue ran dry
	sumSq                    float64
	peak                     int
	since                    time.Time
}

// queueDrops counts frames the playback queue discarded (full, trimmed).
var queueDrops atomic.Int64

var playStatsOut = make(chan playStats, 4)

func init() {
	go func() {
		for s := range playStatsOut {
			rms := 0.0
			if s.played > 0 {
				rms = math.Sqrt(s.sumSq / float64(s.played))
			}
			logs.LogV("[debug] speaker: %d callbacks, %d samples asked, %d played (%s avg, %s peak), %d underruns (%d samples of silence), %d queued frames dropped",
				s.callbacks, s.asked, s.played, levelDB(rms), levelDB(float64(s.peak)), s.underruns, s.silent, queueDrops.Swap(0))
		}
	}()
}

func levelDB(v float64) string {
	if v < 1 {
		return "silent"
	}
	return strconv.Itoa(int(math.Round(20*math.Log10(v/32768)))) + " dBFS"
}

// note adds a callback's numbers; once a second they go to the logger.
func (s *playStats) note(asked int, played []int16, silent int) {
	s.callbacks++
	s.asked += asked
	s.played += len(played)
	for _, v := range played {
		x := int(v)
		if x < 0 {
			x = -x
		}
		s.peak = max(s.peak, x)
		s.sumSq += float64(x) * float64(x)
	}
	if silent > 0 {
		s.underruns++
		s.silent += silent
	}
	now := time.Now()
	if s.since.IsZero() {
		s.since = now
	}
	if now.Sub(s.since) >= time.Second {
		select {
		case playStatsOut <- *s:
		default:
		}
		*s = playStats{since: now}
	}
}

// Frame by frame (DEBUG-TEMP): what happened to each frame of the queue,
// by its media index: started on the speaker, thrown away by the queue,
// and the speaker running dry. The audio thread only sends to a buffered
// channel; package udp joins these with what it knows of the frame.
type PlayEvent struct {
	Kind    byte // PlayStarted, PlayDropped, PlayDry
	Index   uint16
	At      time.Time
	Samples int // the frame's samples; for PlayDry the silence played
}

const (
	PlayStarted = 'p'
	PlayDropped = 'd'
	PlayDry     = 'u' // the speaker had nothing after frame Index
)

var (
	playEvents     = make(chan PlayEvent, 8192)
	playEventsLost atomic.Int64
)

// PlayEvents delivers the frames' fates as they happen.
func PlayEvents() <-chan PlayEvent { return playEvents }

// PlayEventsLost returns, and resets, how many events did not fit.
func PlayEventsLost() int64 { return playEventsLost.Swap(0) }

func tracePlay(e PlayEvent) {
	select {
	case playEvents <- e:
	default:
		playEventsLost.Add(1)
	}
}
