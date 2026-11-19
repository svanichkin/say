package udp

import (
	"fmt"
	"time"
)

// The sender's rate, as WebRTC's rate control, from the receiver's reports
// every rateReportEvery (video_layers.go): what arrived in the last second,
// the loss, the standing queue on the way (the least a datagram waited over
// rateStanding, by the media clock against the shortest trip of the last
// 10 s). Tried on a simulated path in BabeCam (Babe's cmd/babecam), with
// loss in bursts, a link narrowing to 100 kbit/s and a path switch with the
// round trip up to 6 s: the picture stops only while the path carries
// nothing.

// rateReportEvery is how often the receiver reports what arrived.
const rateReportEvery = 100 * time.Millisecond

// rateCtl is the sender's rate (kbit/s) and budget.
type rateCtl struct {
	rate     float64
	tokens   float64 // bytes
	last     time.Duration
	lastCut  time.Duration
	feedback time.Duration // how long a report takes to come back about a frame
	perCell  float64       // bits a second a cell costs on the picture as it is (change frames, as many as go)
	perCellN int           // seconds it was measured on
	secStart time.Duration // the second being measured: since, its bits, its grid, whether the budget held a frame back
	secBits  float64
	secCells int
	secHeld  bool
	cutFrom  float64  // the rate before the last cut
	why      string   // why the last cut, for the log
	sentWin  []sentAt // what went in the last rateSentKeep
}

type sentAt struct {
	at    time.Duration
	bytes int
}

// rateReport is the receiver's report on the last rateReportEvery.
type rateReport struct {
	queue    time.Duration // the standing queue on the way: the least a frame waited in the last rateStanding
	loss1s   float64       // the loss of the last second (a window holds a few frames only)
	bytes1s  int           // what arrived in the last second
	newestAt time.Duration // when the newest frame that came was sent
	none     bool          // no frame came in the window
	silent   bool          // none for rateSilent
}

const (
	rateStart     = 600.0 // kbit/s
	rateMin       = 20.0
	rateMax       = 4000.0
	rateOverQueue = 100 * time.Millisecond // a standing queue this long ...
	rateOverShare = 0.9                    // ... while less than this share of what went arrives: the link is over
	rateStanding  = 500 * time.Millisecond // a keyframe's burst drains within it
	rateCutLoss   = 0.15
	rateHoldQ     = 50 * time.Millisecond
	rateHoldLoss  = 0.05
	rateCut       = 0.85  // of what arrived
	rateUp        = 1.06  // a clean report, up to where the last cut came from
	rateUpSlow    = 1.015 // past it
	rateFloorCap  = 100.0 // the rate may go this far above an idle stream
	rateBurst     = 200 * time.Millisecond
	rateSilent    = 500 * time.Millisecond // no frame this long (one goes every sayHeartbeat at least): the path is gone
	rateSentKeep  = 15 * time.Second
	rateSeries    = 5 * time.Second // cuts this close are one series
)

func newRateCtl() rateCtl { return rateCtl{rate: rateStart, lastCut: -time.Hour, cutFrom: rateMax} }

// report takes a report. The link is over when a standing queue (one a
// keyframe's burst does not make) comes with less arriving than went in the
// same second: then the rate drops to below what arrived, once a feedback
// round. A path that changed to a longer one makes a queue with all that
// went arriving, so no cut; shrinking back, more arrives than went. Loss
// of rateCutLoss or a silent path cuts too; a queue or lighter loss
// (random, not the link's) holds the rate; else it rises, fast up to where
// the last cut came from, slowly past it.
func (c *rateCtl) report(now time.Duration, r rateReport) {
	if r.none && !r.silent {
		return // nothing to tell
	}
	got := float64(r.bytes1s*8) / 1000 // kbit/s
	over := false
	if !r.none {
		c.feedback = now - r.newestAt
		went := c.sentBetween(r.newestAt-time.Second, r.newestAt)
		over = r.queue >= rateOverQueue && got < rateOverShare*went
	}
	switch {
	case r.silent || over || r.loss1s >= rateCutLoss:
		if now-c.lastCut >= max(2*rateReportEvery, c.feedback) {
			if !r.silent && now-c.lastCut >= rateSeries {
				c.cutFrom = c.rate // where a series of cuts starts: what to come back to fast
			}
			c.lastCut = now
			switch {
			case r.silent:
				c.why = "тишина"
			case over:
				c.why = fmt.Sprintf("очередь %d мс, дошло %.0f кбит/с", r.queue.Milliseconds(), got)
			default:
				c.why = fmt.Sprintf("потери %.0f%%", 100*r.loss1s)
			}
			c.why = fmt.Sprintf("%.0f с: %s", now.Seconds(), c.why)
			switch {
			case r.silent:
				c.rate = max(rateMin, c.rate*rateCut)
			case over:
				// To below what arrived, but at most half what went at once: a
				// path that turned longer looks the same for a moment.
				c.rate = max(rateMin, min(c.rate, max(got*rateCut, min(c.rate, c.sending(now))/2)))
			default: // loss: frames of fewer fragments lose less, gently (as WebRTC's)
				c.rate = max(rateMin, c.rate*(1-r.loss1s/2))
			}
		}
	case r.queue >= rateHoldQ && got < c.sentBetween(r.newestAt-time.Second, r.newestAt) || r.loss1s >= rateHoldLoss:
	default:
		up := rateUpSlow
		if c.rate < 0.9*c.cutFrom {
			up = rateUp
		}
		if cap := max(rateFloorCap, 2*c.sending(now)); c.rate < cap {
			c.rate = min(rateMax, c.rate*up, cap) // not past twice what the stream uses
		}
	}
}

// sentBetween is what went from a to b, kbit/s over that time.
func (c *rateCtl) sentBetween(a, b time.Duration) float64 {
	n := 0
	for _, s := range c.sentWin {
		if s.at > a && s.at <= b {
			n += s.bytes
		}
	}
	return float64(n*8) / 1000 / (b - a).Seconds()
}

// sending is what went in the last second, kbit/s.
func (c *rateCtl) sending(now time.Duration) float64 {
	b := 0
	for _, s := range c.sentWin {
		if now-s.at <= time.Second {
			b += s.bytes
		}
	}
	return float64(b*8) / 1000
}

// allow says whether a frame may go at now.
func (c *rateCtl) allow(now time.Duration) bool {
	c.tokens = min(c.tokens+c.rate*1000/8*(now-c.last).Seconds(), c.rate*1000/8*rateBurst.Seconds())
	c.last = now
	return c.tokens > 0
}

// spent takes bytes sent.
func (c *rateCtl) spent(now time.Duration, bytes int) {
	c.tokens -= float64(bytes)
	c.sentWin = append(c.sentWin, sentAt{now, bytes})
	for len(c.sentWin) > 0 && now-c.sentWin[0].at > rateSentKeep {
		c.sentWin = c.sentWin[1:]
	}
}

// measure takes bytes sent of the picture whose cost a cell is measured on
// (the upper layer): its cells, whether a keyframe.
func (c *rateCtl) measure(now time.Duration, bytes, cells int, key bool) {
	if cells != c.secCells {
		c.secStart, c.secBits, c.secCells, c.secHeld = now, 0, cells, false // another grid: measured anew
	}
	if !key {
		c.secBits += float64(bytes * 8)
	}
}

// tick ends a second of measuring what a cell costs: the bits that went (no
// keyframes) over the cells. With frames held back by the budget it costs
// at least that.
func (c *rateCtl) tick(now time.Duration) {
	d := now - c.secStart
	if d < time.Second || c.secCells <= 0 {
		return
	}
	v := c.secBits / d.Seconds() / float64(c.secCells)
	switch {
	case c.perCellN == 0:
		c.perCell = v
	case c.secHeld:
		c.perCell = max(c.perCell, v)
	default:
		c.perCell = 0.5*c.perCell + 0.5*v
	}
	c.perCellN++
	c.secStart, c.secBits, c.secHeld = now, 0, false
}

// owdFloor is the shortest trip of the last 10 s (a path that changed is
// its new self after that), in 1 s buckets.
type owdFloor struct {
	b [10]struct {
		ok  bool
		at  time.Duration // the bucket's second
		min time.Duration
	}
}

func (f *owdFloor) note(now, owd time.Duration) {
	sec := now - now%time.Second
	k := &f.b[int(now/time.Second)%len(f.b)]
	if !k.ok || k.at != sec {
		k.ok, k.at, k.min = true, sec, owd
	} else if owd < k.min {
		k.min = owd
	}
}

func (f *owdFloor) get(now time.Duration) time.Duration {
	m := time.Duration(-1)
	for _, k := range f.b {
		if k.ok && now-k.at < 10*time.Second && (m < 0 || k.min < m) {
			m = k.min
		}
	}
	return max(0, m)
}
