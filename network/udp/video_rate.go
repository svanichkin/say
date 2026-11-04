package udp

import (
	"time"

	"github.com/svanichkin/say/ui"
)

// Dynamic video frame rate. Every video datagram carries a one-byte frame
// counter after the media header. The receiver counts missing counters and
// frames it could not decode, reports the loss to the sender in its pongs
// (network/tcp keepalive), and the sender lowers its frame rate while the
// peer loses frames and raises it again, up to -fps, once the path is clean.
const (
	videoSeqBytes = 1

	videoLossHeavy    = 0.10 // heavy loss: cut the rate by a third
	videoLossLight    = 0.03 // light loss: step down
	videoLossClean    = 0.01 // below this a report counts as clean
	videoFPSStepDown  = 3
	videoFPSStepUp    = 2
	videoCleanReports = 3 // clean reports in a row before stepping up
)

// videoLossMeter measures incoming video loss from the frame counters.
type videoLossMeter struct {
	have     bool
	last     byte
	expected int
	lost     int
	since    time.Time
}

// observe accounts for a received frame counter and any frames skipped before it.
func (m *videoLossMeter) observe(seq byte, now time.Time) {
	if m.since.IsZero() {
		m.since = now
	}
	if !m.have {
		m.have, m.last = true, seq
		m.expected++
		return
	}
	gap := int(seq - m.last) // modulo 256
	switch {
	case gap == 0:
		return // duplicate
	case gap > 128:
		return // reordered or stale: already counted as missing
	}
	m.last = seq
	m.expected += gap
	m.lost += gap - 1
}

// failed counts a received frame that could not be decoded as lost.
func (m *videoLossMeter) failed() {
	m.lost++
}

// report publishes the loss of the last window (about a second) via ui.
func (m *videoLossMeter) report(now time.Time) {
	if m.since.IsZero() || now.Sub(m.since) < time.Second {
		return
	}
	if m.expected > 0 {
		ui.UpdateRxVideoLoss(float64(min(m.lost, m.expected)) / float64(m.expected))
	}
	m.expected, m.lost, m.since = 0, 0, now
}

// Cell hold limits for a lossy path (see maxVideoHold): small pictures reach
// at most videoHoldSmallMax, large ones videoHoldLargeMax, linearly between
// videoHoldSmallCells and videoHoldLargeCells.
const (
	videoHoldSmallMax   = 32
	videoHoldLargeMax   = 128
	videoHoldSmallCells = 1000
	videoHoldLargeCells = 6400
	videoHoldStepUp     = 6 // light loss
	videoHoldStepDown   = 4 // per clean report, back towards -video-hold
)

// maxVideoHold is the highest cell hold for a picture of the given size: a
// large picture tolerates coarser cells than a small one.
func maxVideoHold(cells int) int {
	switch {
	case cells <= videoHoldSmallCells:
		return videoHoldSmallMax
	case cells >= videoHoldLargeCells:
		return videoHoldLargeMax
	}
	return videoHoldSmallMax + (videoHoldLargeMax-videoHoldSmallMax)*(cells-videoHoldSmallCells)/(videoHoldLargeCells-videoHoldSmallCells)
}

// nextVideoHold raises the cell hold (fewer changed cells, smaller frames) while
// the peer loses frames and lowers it back to base on a clean path.
func nextVideoHold(cur, base, maxHold int, loss float64) int {
	maxHold = max(maxHold, base)
	switch {
	case loss >= videoLossHeavy:
		return min(maxHold, max(cur, base)*3/2+8)
	case loss >= videoLossLight:
		return min(maxHold, cur+videoHoldStepUp)
	case loss < videoLossClean:
		return max(base, cur-videoHoldStepDown)
	}
	return cur
}

// nextVideoFPS adapts the sender's frame rate to the peer's reported loss.
// clean counts consecutive clean reports.
func nextVideoFPS(cur, maxFPS int, loss float64, clean *int) int {
	switch {
	case loss >= videoLossHeavy:
		*clean = 0
		return max(minVideoFPS, cur*2/3)
	case loss >= videoLossLight:
		*clean = 0
		return max(minVideoFPS, cur-videoFPSStepDown)
	case loss < videoLossClean:
		*clean++
		if *clean >= videoCleanReports {
			*clean = 0
			return min(maxFPS, cur+videoFPSStepUp)
		}
	default:
		*clean = 0
	}
	return cur
}
