package ui

import (
	"testing"
	"time"
)

// While a call is dialled the status has x under a line: a click on it,
// or Enter, cancels the dial; with nothing dialled there is none.
func TestDialCancelX(t *testing.T) {
	cancelled := make(chan struct{}, 4)
	SetDialCancel(func() { cancelled <- struct{}{} })
	defer SetDialCancel(nil)
	setTermHUD(true)
	defer setTermHUD(false)
	cols, rows := 100, 20
	renderConnectionScreen(cols, rows, "DIALING\n[200::1]:7000")
	statusCancel.Lock()
	row, from, to := statusCancel.row, statusCancel.from, statusCancel.to
	statusCancel.Unlock()
	if row == 0 || to <= from {
		t.Fatal("no x while dialling")
	}
	if !statusCancelHit((from+to)/2, row) || statusCancelHit(from, row-1) {
		t.Fatal("the x's place is not where it is drawn")
	}
	chatKey('\r')
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Enter did not cancel the dial")
	}
	SetDialCancel(nil)
	renderConnectionScreen(cols, rows, "WAITING")
	statusCancel.Lock()
	row = statusCancel.row
	statusCancel.Unlock()
	if row != 0 {
		t.Fatal("an x with nothing dialled")
	}
}
