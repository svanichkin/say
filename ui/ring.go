package ui

import (
	"sync"

	"github.com/svanichkin/say/keys"
	"github.com/svanichkin/say/lang"
)

// A call coming in rings on the status screen: its heading, who calls and
// their address, and under a line ✓ (answer) and x (decline), as a menu's
// last items: ↑ and ↓ choose, Enter or a click does it, Esc declines.
// While it rings the home screen is not shown.

var ringing struct {
	sync.Mutex
	on     bool
	sel    int    // 0 ✓, 1 x
	before string // the status message it replaced
	answer func(bool)
}

// Ringing shows a call from who (lines: a name, an address) ringing; answer
// gets the user's choice, once (true: ✓).
func Ringing(who string, answer func(bool)) {
	ringing.Lock()
	ringing.on, ringing.sel, ringing.answer = true, 0, answer
	ringing.before = currentStatusMessage()
	ringing.Unlock()
	SetStatusMessage(lang.T(lang.IncomingCall) + "\n" + who)
	if f := showWindow.get(); f != nil {
		f() // a hidden window comes back to ring
	}
}

// showWindow brings the window back (SetShowWindow).
var showWindow atomicFunc

// SetShowWindow is what brings a hidden window back when a call comes in.
func SetShowWindow(f func()) { showWindow.set(f) }

// RingingOver takes the ringing call off the screen: answered, declined, or
// its caller gone.
func RingingOver() {
	ringing.Lock()
	was, before := ringing.on, ringing.before
	ringing.on, ringing.answer = false, nil
	ringing.Unlock()
	if was {
		SetStatusMessage(before)
	}
}

func ringingOn() bool {
	ringing.Lock()
	defer ringing.Unlock()
	return ringing.on
}

// ringAnswer gives the user's choice to the ringing call.
func ringAnswer(yes bool) {
	ringing.Lock()
	answer := ringing.answer
	ringing.answer = nil
	ringing.Unlock()
	if answer != nil {
		answer(yes)
	}
}

// statusAction is an item under the status screen's line: its mark, the
// mark's colour, what it does.
type statusAction struct {
	mark   string
	colour [3]int
	do     func()
}

// statusActions are the status screen's items under its line and the one
// chosen: ✓ and x while a call rings, x while one is dialled.
func statusActions() ([]statusAction, int) {
	if ChatConnected() {
		return nil, 0
	}
	ringing.Lock()
	on, sel := ringing.on, ringing.sel
	ringing.Unlock()
	if on {
		return []statusAction{
			{"✓", homeMarkColour, func() { ringAnswer(true) }},
			{"x", homeCloseColour, func() { ringAnswer(false) }},
		}, sel
	}
	if dialCancel.get() != nil {
		return []statusAction{{"x", homeCloseColour, cancelDial}}, 0
	}
	return nil, 0
}

// statusKey handles a key on the status screen's items; false: not theirs.
func statusKey(r rune) bool {
	acts, sel := statusActions()
	if len(acts) == 0 {
		return false
	}
	switch r {
	case keys.Up, keys.Down:
		ringing.Lock()
		if ringing.on {
			if r == keys.Up {
				ringing.sel = max(0, ringing.sel-1)
			} else {
				ringing.sel = min(len(acts)-1, ringing.sel+1)
			}
		}
		ringing.Unlock()
		RequestRedraw()
	case '\r', '\n':
		acts[sel].do()
	case keys.Esc:
		acts[len(acts)-1].do() // x
	default:
		return false
	}
	return true
}

// The status screen's items as drawn: their row, columns.
var statusItems struct {
	sync.Mutex
	at []statusItemAt
}

type statusItemAt struct{ row, from, to int }

func setStatusItems(at []statusItemAt) {
	statusItems.Lock()
	statusItems.at = at
	statusItems.Unlock()
}

// statusClick does the status screen's item clicked, if any.
func statusClick(col, row int) bool {
	statusItems.Lock()
	at := append([]statusItemAt(nil), statusItems.at...)
	statusItems.Unlock()
	acts, _ := statusActions()
	for i, a := range at {
		if i < len(acts) && row == a.row && col >= a.from && col <= a.to {
			acts[i].do()
			return true
		}
	}
	return false
}
