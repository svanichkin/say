package udp

import (
	"github.com/svanichkin/say/logs"
	"time"

	"github.com/svanichkin/say/ui"
)

// Every video datagram carries a one-byte counter after the media header.
// The receiver counts missing counters and frames it could not decode, and
// shows the loss (and reports it in its pongs, network/tcp keepalive); the
// sender adapts by its rate (video_ratectl.go, video_layers.go).
const videoSeqBytes = 1

// videoLossMeter measures incoming video loss from the frame counters.
// The datagrams expected (as the counter goes) against those that came: a
// late one counts as come (the bands of a frame go together and may arrive
// out of order).
type videoLossMeter struct {
	have     bool
	last     byte
	expected int
	got      int
	lost     int // frames that came but were of no use (undecodable, too late)
	since    time.Time
}

// observe accounts for a received datagram counter.
func (m *videoLossMeter) observe(seq byte, now time.Time) {
	if m.since.IsZero() {
		m.since = now
	}
	m.got++
	if !m.have {
		m.have, m.last = true, seq
		m.expected++
		return
	}
	if gap := int(seq - m.last); gap > 0 && gap <= 128 { // modulo 256; else late
		m.last = seq
		m.expected += gap
	}
}

// failed counts a received frame that could not be decoded as lost.
func (m *videoLossMeter) failed() {
	logs.LogV("[debug] video: a frame could not be decoded") // DEBUG-TEMP
	m.lost++
}

// late counts received frames that were never shown because they arrived
// after their sound had played. To the viewer they are as lost as frames the
// network dropped, and the remedy is the same: a smaller stream queues less
// on the way. Frames skipped because they arrived in a burst with newer ones
// are not counted: the path delivers unevenly, and a smaller stream does not
// change that.
func (m *videoLossMeter) late(n int) {
	if n > 0 { // DEBUG-TEMP
		logs.LogV("[debug] video: %d frames too late to show", n)
	}
	m.lost += max(0, n)
}

// report publishes the loss of the last window (about a second) via ui.
func (m *videoLossMeter) report(now time.Time) {
	if m.since.IsZero() || now.Sub(m.since) < time.Second {
		return
	}
	if m.expected > 0 {
		lost := max(0, m.expected-m.got) + m.lost
		ui.UpdateRxVideoLoss(float64(min(lost, m.expected)) / float64(m.expected))
	}
	m.expected, m.got, m.lost, m.since = 0, 0, 0, now
}

// The cell hold on a clean path grows with the frame (baseVideoHold): -video-hold
// up to videoHoldBaseCells, twice it from videoHoldBaseLarge, linearly between.
// Past the camera's own pixels (352x288 in 4x8 cells: 88x36) finer cells
// sample the same pixels again and mostly carry the camera's noise: on two
// recorded calls (cmd/videosim) 160x65 at hold 10 took 463 and 511 kbit/s,
// at hold 20 288 and 281 (88x36 at hold 10: 158 and 176), and looked as good
// by eye.
const (
	videoHoldBaseCells = 88 * 36
	videoHoldBaseLarge = 160 * 65
)

// baseVideoHold is the cell hold for a frame of the given size on a clean
// path, from hold (-video-hold).
func baseVideoHold(hold, cells int) int {
	switch {
	case cells <= videoHoldBaseCells:
		return hold
	case cells >= videoHoldBaseLarge:
		return 2 * hold
	}
	return hold + hold*(cells-videoHoldBaseCells)/(videoHoldBaseLarge-videoHoldBaseCells)
}
