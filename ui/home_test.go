package ui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/svanichkin/say/keys"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// lineAt is the text drawn on a row of a screen (1-based), the escapes
// dropped and the cursor moves followed.
func rowsOf(screen string, rows, cols int) []string {
	grid := make([][]rune, rows+1)
	for i := range grid {
		grid[i] = []rune(strings.Repeat(" ", cols+1))
	}
	row, col := 1, 1
	move := regexp.MustCompile(`^\x1b\[(\d+);(\d+)H`)
	for len(screen) > 0 {
		if m := move.FindStringSubmatch(screen); m != nil {
			row, col = atoi(m[1]), atoi(m[2])
			screen = screen[len(m[0]):]
			continue
		}
		if loc := sgr.FindStringIndex(screen); loc != nil && loc[0] == 0 {
			screen = screen[loc[1]:]
			continue
		}
		r := []rune(screen)[0]
		screen = screen[len(string(r)):]
		if row >= 1 && row <= rows && col >= 1 && col <= cols {
			grid[row][col] = r
		}
		col++
	}
	out := make([]string, rows+1)
	for i := range grid {
		out[i] = strings.TrimRight(string(grid[i][1:]), " ")
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// The chosen line is on its colour on the middle row; the arrows move the
// list under it; Enter opens a column in the middle with the one before to
// its left; a field is typed in as soon as it is chosen and set as it is
// left; Esc goes back; x in the first column quits.
func TestHomeMenu(t *testing.T) {
	var set, quit, done string
	devices := func() []HomeItem {
		return []HomeItem{
			{Text: "home  200::1", Do: func() { done = "called" }},
			{Key: "label", Field: &HomeField{Value: set, Placeholder: "label", Set: func(v string) { set = v }}},
			{Key: "port", Field: &HomeField{Placeholder: "port"}},
			{Back: true}, {Close: true},
		}
	}
	root := func() []HomeItem {
		return []HomeItem{
			{Text: "Alice Ivanovna", Open: devices},
			{Text: "Bob Justingate Tailor", Open: devices, Start: true},
			{Text: "Carol"},
			{Close: true},
		}
	}
	SetHome(root, nil, func() { quit = "quit" })
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()

	cols, rows := 80, 21
	mid := (termBorder + 1 + rows - termBorder) / 2
	screen := rowsOf(renderHomeScreen(cols, rows), rows, cols)
	if !strings.Contains(screen[mid], " Bob Justingate Tailor ") {
		t.Fatalf("the start item is not chosen on the middle row %d: %q", mid, screen[mid])
	}
	if !strings.Contains(screen[mid-1], "Alice Ivanovna") || !strings.Contains(screen[mid+1], "Carol") {
		t.Fatalf("the list around it: %q / %q", screen[mid-1], screen[mid+1])
	}
	if !strings.Contains(screen[mid], "home  200::1") {
		t.Errorf("what Bob opens is not shown to the right: %q", screen[mid])
	}

	homeKey(keys.Up)
	screen = rowsOf(renderHomeScreen(cols, rows), rows, cols)
	if !strings.Contains(screen[mid], " Alice Ivanovna ") || !strings.Contains(screen[mid+1], "Bob") {
		t.Fatalf("Up did not move the list: %q", screen[mid])
	}

	homeKey('\r')
	screen = rowsOf(renderHomeScreen(cols, rows), rows, cols)
	m := screen[mid]
	if !strings.Contains(m, " home  200::1") || !strings.Contains(m, " Alice Ivanovna ") ||
		strings.Index(m, "Alice") > strings.Index(m, "home") {
		t.Fatalf("the person's column: %q", m)
	}
	if strings.Contains(screen[mid+1], "Bob") && !strings.Contains(screen[mid+1], "label") {
		t.Errorf("row below: %q", screen[mid+1])
	}

	homeKey(keys.Down) // the label field: typed in at once
	for _, r := range "phone" {
		homeKey(r)
	}
	screen = rowsOf(renderHomeScreen(cols, rows), rows, cols)
	if !strings.HasSuffix(strings.TrimRight(screen[mid], " │┃╏║"), " phone_") { // in its column's frame
		t.Fatalf("the field being typed in: %q", screen[mid])
	}
	homeKey(keys.Down) // leaving it sets it
	if set != "phone" {
		t.Fatalf("the field was set to %q", set)
	}
	home.Lock()
	editing, cur := home.editing, home.stack[len(home.stack)-1].cur
	home.Unlock()
	if editing || cur != 2 {
		t.Errorf("after leaving the field: typing %v, at %d", editing, cur)
	}
	homeKey(keys.Up) // back on it: typing goes on from its value
	homeKey('!')
	homeKey('\r') // Enter sets it and goes on to the next line
	if set != "phone!" {
		t.Fatalf("Enter set %q", set)
	}
	homeKey(keys.Esc) // back to the names
	homeKey(keys.Down)
	homeKey(keys.Down)
	homeKey(keys.Down)
	homeKey('\r')
	if quit != "quit" {
		t.Fatal("x in the first column did not quit")
	}
	_ = done
}

// A click on an item in the middle does it; one to the left goes back.
func TestHomeClicks(t *testing.T) {
	var done bool
	root := func() []HomeItem {
		return []HomeItem{
			{Text: "Alice", Open: func() []HomeItem {
				return []HomeItem{{Text: "call", Do: func() { done = true }}, {Back: true}}
			}},
			{Text: "Bob"},
		}
	}
	SetHome(root, nil, nil)
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()
	cols, rows := 60, 15
	mid := (termBorder + 1 + rows - termBorder) / 2
	screen := rowsOf(renderHomeScreen(cols, rows), rows, cols)
	at := cellIndex(screen[mid], "Alice")
	homeClick(at+1, mid)
	screen = rowsOf(renderHomeScreen(cols, rows), rows, cols)
	at = cellIndex(screen[mid], "call")
	if at < 0 {
		t.Fatalf("the click did not open Alice: %q", screen[mid])
	}
	homeClick(at+1, mid)
	if !done {
		t.Fatal("a click on call did not call")
	}
	screen = rowsOf(renderHomeScreen(cols, rows), rows, cols)
	at = cellIndex(screen[mid], "Alice")
	homeClick(at+1, mid)
	home.Lock()
	depth := len(home.stack)
	home.Unlock()
	if depth != 1 {
		t.Fatalf("a click on the column to the left left %d columns", depth)
	}
}

// An item that is opened and done: Enter opens its column and does it;
// showing its column to the right does not.
func TestHomeEnter(t *testing.T) {
	entered := 0
	SetHome(func() []HomeItem {
		return []HomeItem{{Text: "book", Open: func() []HomeItem { return []HomeItem{{Text: "card"}} }, Enter: func() { entered++ }}}
	}, nil, nil)
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()
	renderHomeScreen(60, 15)
	if entered != 0 {
		t.Fatal("drawing it did it")
	}
	homeKey('\r')
	home.Lock()
	depth := len(home.stack)
	home.Unlock()
	if entered != 1 || depth != 2 {
		t.Fatalf("Enter: done %d times, %d columns", entered, depth)
	}
}

// What the chosen item opens is shown to the right at the item it opens at
// (our card, ticked), not at its top.
func TestHomePreviewAtStart(t *testing.T) {
	SetHome(func() []HomeItem {
		return []HomeItem{{Text: "my card", Open: func() []HomeItem {
			return []HomeItem{{Text: "Alice"}, {Text: "Bob"}, {Text: "Me", Tick: true, Start: true}, {Text: "Zed"}}
		}}}
	}, nil, nil)
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()
	cols, rows := 100, 15
	mid := (termBorder + 1 + rows - termBorder) / 2
	screen := rowsOf(renderHomeScreen(cols, rows), rows, cols)
	if !strings.Contains(screen[mid], "✓ Me") || !strings.Contains(screen[mid-2], "Alice") {
		t.Fatalf("the column to the right: %q / %q", screen[mid-2], screen[mid])
	}
}

// x with CloseTo goes back to the column with that item, not the first.
func TestHomeCloseTo(t *testing.T) {
	var deeper func(n int) []HomeItem
	deeper = func(n int) []HomeItem {
		return []HomeItem{{Text: "down", Open: func() []HomeItem { return deeper(n + 1) }}, {Close: true, CloseTo: "path"}}
	}
	SetHome(func() []HomeItem {
		return []HomeItem{{Text: "settings", Open: func() []HomeItem {
			return []HomeItem{{Key: "path", Text: "~/Contacts", Open: func() []HomeItem { return deeper(0) }}}
		}}}
	}, nil, nil)
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()
	homeKey('\r') // settings
	homeKey('\r') // path
	homeKey('\r') // down
	homeKey('\r') // down
	homeKey(keys.Down)
	homeKey('\r') // x
	home.Lock()
	depth, top := len(home.stack), home.stack[len(home.stack)-1].items[0].Text
	home.Unlock()
	if depth != 2 || top != "~/Contacts" {
		t.Fatalf("x went to %d columns deep, at %q", depth, top)
	}
}

// A leaf item whose column would be only < and x is not opened: its Enter
// is done.
func TestHomeLeaf(t *testing.T) {
	entered := 0
	SetHome(func() []HomeItem {
		return []HomeItem{{Text: "empty", Leaf: true, Open: func() []HomeItem { return []HomeItem{{Back: true}, {Close: true}} }, Enter: func() { entered++ }}}
	}, nil, nil)
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()
	homeKey('\r')
	home.Lock()
	depth := len(home.stack)
	home.Unlock()
	if entered != 1 || depth != 1 {
		t.Fatalf("done %d, %d columns", entered, depth)
	}
}

// A form with a group none filled: the first try to leave makes the empty
// fields blink and stays; the second leaves. A value just typed counts.
func TestHomeNeed(t *testing.T) {
	var name, addr string
	SetHome(func() []HomeItem {
		return []HomeItem{{Text: "+", Open: func() []HomeItem {
			return []HomeItem{
				{Key: "given", Field: &HomeField{Value: name, Placeholder: "name", Need: "name", Set: func(v string) { name = v }}},
				{Key: "addr", Field: &HomeField{Value: addr, Placeholder: "address", Need: "addr", Set: func(v string) { addr = v }}},
				{Back: true},
			}
		}}}
	}, nil, nil)
	ShowHome(true)
	defer func() { SetHome(nil, nil, nil); ShowHome(false) }()
	depth := func() int { home.Lock(); defer home.Unlock(); return len(home.stack) }
	homeKey('\r') // the form
	for _, r := range "Anna" {
		homeKey(r)
	}
	homeKey(keys.Left) // the name typed counts; the address is empty: stays
	if depth() != 2 {
		t.Fatal("left the form with the address empty")
	}
	home.Lock()
	lit, litName := home.need["addr"], home.need["given"]
	home.Unlock()
	if !lit || litName {
		t.Fatalf("lit: address %v, name %v", lit, litName)
	}
	if name != "Anna" {
		t.Fatalf("the name typed was not set: %q", name)
	}
	homeKey(keys.Left) // the second try leaves
	if depth() != 1 {
		t.Fatal("the second try did not leave")
	}
}

// cellIndex is where sub starts in a screen row, in cells (the frames'
// lines and the arrows are a cell each, more bytes); -1 if not there.
func cellIndex(row, sub string) int {
	at := strings.Index(row, sub)
	if at < 0 {
		return -1
	}
	return utf8.RuneCountInString(row[:at])
}
