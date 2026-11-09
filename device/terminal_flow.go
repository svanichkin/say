package device

import (
	"sync"
	"sync/atomic"
	"time"
)

// Terminal flow control. A terminal reads its input into its own buffer and
// draws from there; a hidden window keeps reading but draws late, so frames
// pile up inside the terminal and, back on screen, video runs seconds behind
// the sound and never catches up. After each frame we ask for a status report
// (DSR, ESC[5n): the terminal answers ESC[0n only once it has processed
// everything written before the question, so while an answer is outstanding
// the next frame waits and at most one frame is ever queued in the terminal.

// terminalStatusQuery is DSR 5 (device status report).
const terminalStatusQuery = "\x1b[5n"

// terminalProbeFrames is how many frames ask for a report before the terminal
// has answered once; a terminal that never answers is never waited for.
const terminalProbeFrames = 8

// terminalAckTimeout bounds the wait for an answer, so a lost one cannot
// freeze the picture.
const terminalAckTimeout = 2 * time.Second

var (
	terminalFlowOn    atomic.Bool  // the terminal has answered at least once
	terminalProbes    atomic.Int32 // queries sent before the first answer
	terminalPending   atomic.Int32 // queries not answered yet
	terminalProgress  atomic.Int64 // UnixNano of the last query or answer
	terminalSentAt    atomic.Int64 // UnixNano of the newest query
	terminalAckNanos  atomic.Int64 // sum of answer delays (stats)
	terminalAcks      atomic.Int64 // answers (stats)
	terminalAckMu     sync.Mutex
	terminalAckNotify func()
)

// terminalQueryAfterFrame returns the query to append to a frame, or "".
func terminalQueryAfterFrame() string {
	if !terminalFlowOn.Load() && terminalProbes.Add(1) > terminalProbeFrames {
		return ""
	}
	terminalPending.Add(1)
	now := time.Now().UnixNano()
	terminalProgress.Store(now)
	terminalSentAt.Store(now)
	return terminalStatusQuery
}

// TerminalBusy reports whether the terminal has not yet processed the last
// frame, so the next one should wait for TerminalAck. Every query is counted:
// frames written before the first answer each carry one, and only the last
// answer means the terminal has caught up.
func TerminalBusy() bool {
	if !terminalFlowOn.Load() || terminalPending.Load() <= 0 {
		return false
	}
	if time.Since(time.Unix(0, terminalProgress.Load())) < terminalAckTimeout {
		return true
	}
	terminalPending.Store(0) // answers were lost; stop waiting for them
	return false
}

// TerminalAck records the terminal's status report (ESC[0n), read by the
// input loop.
func TerminalAck() {
	terminalFlowOn.Store(true)
	left := terminalPending.Add(-1)
	if left < 0 {
		terminalPending.Store(0)
	}
	if left <= 0 {
		terminalAckNanos.Add(time.Now().UnixNano() - terminalSentAt.Load())
		terminalAcks.Add(1)
	}
	terminalProgress.Store(time.Now().UnixNano())
	terminalAckMu.Lock()
	notify := terminalAckNotify
	terminalAckMu.Unlock()
	if notify != nil {
		notify()
	}
}

// SetTerminalAckHandler sets a function called after each TerminalAck.
func SetTerminalAckHandler(fn func()) {
	terminalAckMu.Lock()
	terminalAckNotify = fn
	terminalAckMu.Unlock()
}

// TerminalAckStats returns and resets the number of caught-up answers and
// their mean delay since the previous call.
func TerminalAckStats() (acks int64, mean time.Duration) {
	acks = terminalAcks.Swap(0)
	total := terminalAckNanos.Swap(0)
	if acks > 0 {
		mean = time.Duration(total / acks)
	}
	return acks, mean
}
