package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/keys"
)

func drawnItems() []statusItemAt {
	statusItems.Lock()
	defer statusItems.Unlock()
	return append([]statusItemAt(nil), statusItems.at...)
}

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
	at := drawnItems()
	if len(at) != 1 || at[0].to <= at[0].from {
		t.Fatal("no x while dialling")
	}
	if !statusClick((at[0].from+at[0].to)/2, at[0].row) {
		t.Fatal("the x's place is not where it is drawn")
	}
	<-cancelled
	if statusClick(at[0].from, at[0].row-1) {
		t.Fatal("a click above the x cancels")
	}
	chatKey('\r')
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Enter did not cancel the dial")
	}
	SetDialCancel(nil)
	renderConnectionScreen(cols, rows, "WAITING")
	if len(drawnItems()) != 0 {
		t.Fatal("an x with nothing dialled")
	}
}

// A call ringing has ✓ and x under a line, ✓ chosen: ↓ chooses x, Enter
// does the chosen one, Esc declines, a click does the one clicked; the
// answer is given once, and the screen it replaced comes back.
func TestRinging(t *testing.T) {
	setTermHUD(true)
	defer setTermHUD(false)
	cols, rows := 100, 24
	SetStatusMessage("WAITING")
	defer SetStatusMessage("")
	for _, tc := range []struct {
		name string
		keys []rune
		want bool
	}{{"enter", []rune{'\r'}, true}, {"down enter", []rune{keys.Down, '\r'}, false}, {"esc", []rune{keys.Esc}, false}} {
		got := make(chan bool, 2)
		Ringing("Alice\n200::1", func(yes bool) { got <- yes })
		if HomeShown() {
			t.Fatal("home shown while ringing")
		}
		screen := renderConnectionScreen(cols, rows, currentStatusMessage())
		if !strings.Contains(screen, "Alice") || !strings.Contains(screen, "✓") {
			t.Fatalf("%s: the ringing screen lacks who or ✓", tc.name)
		}
		if at := drawnItems(); len(at) != 2 || at[1].row <= at[0].row {
			t.Fatalf("%s: items %v", tc.name, at)
		}
		for _, r := range tc.keys {
			chatKey(r)
		}
		chatKey('\r') // a second answer is not given
		if yes := <-got; yes != tc.want || len(got) != 0 {
			t.Fatalf("%s: answered %v, want %v once", tc.name, yes, tc.want)
		}
		RingingOver()
		if currentStatusMessage() != "WAITING" {
			t.Fatalf("%s: status %q after ringing", tc.name, currentStatusMessage())
		}
	}
	got := make(chan bool, 1)
	Ringing("Bob", func(yes bool) { got <- yes })
	renderConnectionScreen(cols, rows, currentStatusMessage())
	at := drawnItems()
	statusClick(at[1].from, at[1].row)
	if <-got {
		t.Fatal("a click on x answered")
	}
	RingingOver()
}
