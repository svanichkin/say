package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/svanichkin/say/keys"
)

// The home screen: Say waiting for a call, its contacts and its settings as
// a menu of columns, as SSHS draws its list. The column being chosen in is
// in the middle of the screen, its chosen line always on the middle row:
// the arrows move the list under it. The column it was opened from stands
// to its left (only the item chosen there bright, in the chosen line's
// colour; a long name runs off the left edge), and what the chosen item
// opens is shown faint to the right.

// HomeItem is a line of the home screen's menu.
type HomeItem struct {
	Key    string // keeps the item chosen when the menu is built again ("": Text)
	Text   string
	Colour [3]int // the chosen line's colour (none: ours)
	Tinted bool   // Colour is set
	Start  bool   // chosen when its column opens
	Bottom bool   // with < and x, under the line (+)
	Tick   bool   // the one chosen of its kind: a green ✓ before it
	Online bool   // a person whose Say answers: a green ● before them
	// Mark is a mark of its own before it, in MarkColour (a call view's
	// menu: ✓ on, x off, → opens something else).
	Mark       string
	MarkColour [3]int

	Open  func() []HomeItem // opens a column of its own (also to show it: no side effects)
	Enter func()            // done as the column is opened by the user
	Leave func()            // done as the column it opened is closed (under the menu's lock: no menu calls)
	// Leaf: a column with nothing but < and x is not opened, only Enter
	// done (a folder without folders).
	Leaf  bool
	Do    func()     // what Enter does
	Flash string     // shown in place of Text for a moment after Do
	Field *HomeField // an editable value
	// Need and Filled: an item (not a field) of a group to fill before the
	// form is left (a field's group is its HomeField.Need, its value its
	// filling): the device a person needs, its "+".
	Need   string
	Filled bool
	Back   bool // "<": the column before
	Close  bool // "x": back to the first column, or quit Say from it
	// CloseTo: "x" goes back to the column with this item (where a long
	// way down, the folders, began) instead of the first one.
	CloseTo string
}

// HomeField is an editable line: its value, or the placeholder (faint)
// while it is empty.
type HomeField struct {
	Value       string
	Placeholder string
	Set         func(v string) // Enter: the value typed
	Suffix      string         // after the value when it is not being edited (a tick)
	// Submit: Enter does this with what was typed (instead of going on to
	// the next line), and the field is empty again; leaving it keeps nothing.
	Submit func(v string)
	// Need names a group of fields of which one must be filled before
	// the form is left: the first try to leave makes the empty ones blink
	// and stays; the second leaves.
	Need string
}

func (it HomeItem) key() string {
	if it.Key != "" {
		return it.Key
	}
	return it.Text
}

type homeColumn struct {
	items []HomeItem
	cur   int
}

type homeClickArea struct {
	row, c0, c1 int // 1-based row, columns c0..c1
	level, item int // the column (len(stack): the faint one to the right)
}

var home struct {
	sync.Mutex
	root    func() []HomeItem
	paste   func(s string) // a paste outside a field: an address to call
	quit    func()
	shown   bool
	stack   []homeColumn
	editing bool   // the chosen field has a value typed (edit), not yet set
	editKey string // which field (its column and key)
	pendKey string // a field set, its value not yet in the menu (until it is built again)
	pendVal string

	need      map[string]bool // the fields lit up: to fill before leaving the form
	needAt    time.Time
	needFor   string // the form warned (a second try leaves it)
	edit      string
	flashAt   string // the key of the item flashing
	flashText string // what it shows ("": its Flash)
	flashTo   time.Time
	clicks    []homeClickArea
	typed     string
	typedAt   time.Time

	animFrom int       // the columns' offset as the slide began (cells)
	animAt   time.Time // when it began
	shift    int       // the offset of this drawing
}

// SetHome gives the home screen its first column, what a pasted address
// does, and what "x" in the first column does (quit).
func SetHome(root func() []HomeItem, paste func(string), quit func()) {
	home.Lock()
	home.root, home.paste, home.quit = root, paste, quit
	home.stack = nil
	if root != nil {
		home.stack = []homeColumn{openColumn(root())}
	}
	home.editing = false
	home.Unlock()
	RequestRedraw()
}

// ShowHome shows the home screen when there is no call (false: the status
// screen, while a call is dialled).
func ShowHome(on bool) {
	home.Lock()
	home.shown = on
	home.Unlock()
	RequestRedraw()
}

// HomeShown reports whether the home screen is what the screen shows.
func HomeShown() bool {
	if ChatConnected() {
		return false
	}
	home.Lock()
	defer home.Unlock()
	return home.shown && home.root != nil
}

// HomeRefresh builds the menu again (the contacts changed), each column at
// the item it was on; a column whose item is gone closes.
func HomeRefresh() {
	home.Lock()
	refreshLocked()
	home.Unlock()
	RequestRedraw()
}

// HomeFlash shows text in place of the item key for a moment (a reason it
// could not be done).
func HomeFlash(key, text string) {
	home.Lock()
	home.flashAt, home.flashText, home.flashTo = key, text, time.Now().Add(3*time.Second)
	home.Unlock()
	time.AfterFunc(3100*time.Millisecond, RequestRedraw)
	RequestRedraw()
}

// HomeBackTo builds the menu again and goes back to the nearest column with
// the item key, at that item (the first column if none has it).
func HomeBackTo(key string) {
	home.Lock()
	refreshLocked()
	home.editing = false
	for len(home.stack) > 0 {
		top := &home.stack[len(home.stack)-1]
		found := false
		for i, it := range top.items {
			if it.key() == key {
				top.cur, found = i, true
				break
			}
		}
		if found || len(home.stack) == 1 {
			break
		}
		truncateLocked(len(home.stack) - 1)
	}
	home.Unlock()
	RequestRedraw()
}

// HomeDescend opens, from the current column, the items with these keys
// one after another, each column at the next (their Enter not done): a
// path shown from its top.
func HomeDescend(keys []string) {
	home.Lock()
	for _, k := range keys {
		if len(home.stack) == 0 {
			break
		}
		top := &home.stack[len(home.stack)-1]
		at := -1
		for i, it := range top.items {
			if it.key() == k {
				at = i
				break
			}
		}
		if at < 0 || top.items[at].Open == nil {
			break
		}
		top.cur = at
		home.stack = append(home.stack, openColumn(top.items[at].Open()))
	}
	home.Unlock()
	RequestRedraw()
}

// HomeOpenKey builds the menu again and opens, in the middle column, the
// item key (its Enter not done): a form's new part, made and entered at
// once.
func HomeOpenKey(key string) {
	home.Lock()
	refreshLocked()
	home.editing = false
	if len(home.stack) > 0 {
		top := &home.stack[len(home.stack)-1]
		for i, it := range top.items {
			if it.key() == key && it.Open != nil {
				top.cur = i
				pushLocked(openColumn(it.Open()))
				break
			}
		}
	}
	home.Unlock()
	RequestRedraw()
}

// HomeReset goes back to the first column.
func HomeReset() {
	home.Lock()
	if home.root != nil {
		home.stack = []homeColumn{openColumn(home.root())}
	}
	home.editing = false
	home.Unlock()
	RequestRedraw()
}

func refreshLocked() {
	if home.root == nil {
		return
	}
	home.pendKey = "" // what was set is in the values now
	old := home.stack
	items := home.root()
	var stack []homeColumn
	for i := range old {
		col := homeColumn{items: items, cur: max(0, min(old[i].cur, len(items)-1))}
		was := ""
		if old[i].cur < len(old[i].items) {
			was = old[i].items[old[i].cur].key()
			for j, it := range items {
				if it.key() == was {
					col.cur = j
					break
				}
			}
		}
		stack = append(stack, col)
		if i+1 >= len(old) || len(items) == 0 {
			break
		}
		it := items[col.cur]
		if it.key() != was || it.Open == nil {
			break // the item that column came from is gone
		}
		items = it.Open()
	}
	if len(stack) == 0 {
		stack = []homeColumn{openColumn(items)}
	}
	home.stack = stack
	if home.editing {
		if it, ok := currentLocked(); !ok || it.Field == nil {
			home.editing = false
		}
	}
}

func openColumn(items []HomeItem) homeColumn {
	col := homeColumn{items: items}
	for i, it := range items {
		if it.Start {
			col.cur = i
			break
		}
	}
	return col
}

func currentLocked() (HomeItem, bool) {
	if len(home.stack) == 0 {
		return HomeItem{}, false
	}
	col := home.stack[len(home.stack)-1]
	if col.cur < 0 || col.cur >= len(col.items) {
		return HomeItem{}, false
	}
	return col.items[col.cur], true
}

// homeKey is a key while the home screen is shown.
func homeKey(r rune) {
	home.Lock()
	act := homeKeyLocked(r)
	home.Unlock()
	if act != nil {
		act()
		HomeRefresh()
	}
	RequestRedraw()
}

// homeKeyLocked handles r; what it returns runs after the lock (an action
// may build the menu again).
func homeKeyLocked(r rune) func() {
	if len(home.stack) == 0 {
		return nil
	}
	top := &home.stack[len(home.stack)-1]
	it, ok := currentLocked()
	field := ok && it.Field != nil
	// A field is typed in as soon as it is chosen; leaving it (the arrows,
	// Esc, Enter, a click) sets what was typed.
	switch r {
	case keys.Up:
		set := setFieldLocked()
		if top.cur > 0 {
			top.cur--
		}
		return set
	case keys.Down:
		set := setFieldLocked()
		if top.cur < len(top.items)-1 {
			top.cur++
		}
		return set
	case keys.Left, keys.Esc:
		leave := mayLeaveLocked()
		set := setFieldLocked()
		if leave {
			popLocked()
		}
		return set
	case 0x7f, 0x08:
		if !field {
			if mayLeaveLocked() {
				popLocked()
			}
			return nil
		}
		typeLocked(it)
		if home.edit != "" {
			_, n := utf8.DecodeLastRuneInString(home.edit)
			home.edit = home.edit[:len(home.edit)-n]
		}
	case '\r', '\n':
		if !field {
			return activateLocked()
		}
		if submit := it.Field.Submit; submit != nil {
			// A field that does something with what was typed (an address
			// called): done, and the field is empty again.
			v := strings.TrimSpace(it.Field.Value)
			if home.editing && home.editKey == fieldKeyLocked(it) {
				v = strings.TrimSpace(home.edit)
			}
			home.editing, home.edit = false, ""
			return func() { submit(v) }
		}
		set := setFieldLocked() // and on to the next line
		if top.cur < len(top.items)-1 {
			top.cur++
		}
		return set
	case keys.Right:
		if !field {
			return activateLocked()
		}
	default:
		if r < 0x20 || unicode.IsControl(r) || keys.Special(r) {
			return nil
		}
		if field {
			typeLocked(it)
			home.edit += string(r)
			return nil
		}
		jumpLocked(r)
	}
	return nil
}

// The fields to fill blink, as a character hit in a game: shown and gone
// in turn, homeBlinkEvery each, homeBlinks times gone.
const (
	homeBlinkEvery = 90 * time.Millisecond
	homeBlinks     = 4
	homeNeedTime   = 2 * homeBlinks * homeBlinkEvery
)

// mayLeaveLocked reports whether the middle column may be left: yes,
// unless it is a form with a group of fields none filled (Need) and this is
// the first try, which makes them blink instead.
func mayLeaveLocked() bool {
	if len(home.stack) == 0 {
		return true
	}
	level := len(home.stack)
	top := home.stack[level-1]
	filled := map[string]bool{}
	groupOf := func(it HomeItem) string {
		if it.Field != nil {
			return it.Field.Need
		}
		return it.Need
	}
	for _, it := range top.items {
		g := groupOf(it)
		if g == "" {
			continue
		}
		if it.Field == nil {
			filled[g] = filled[g] || it.Filled
			continue
		}
		v, k := it.Field.Value, strconv.Itoa(level)+"/"+it.key()
		if k == home.pendKey {
			v = home.pendVal
		}
		if home.editing && k == home.editKey {
			v = strings.TrimSpace(home.edit)
		}
		filled[g] = filled[g] || v != ""
	}
	need := map[string]bool{}
	for _, it := range top.items {
		if g := groupOf(it); g != "" && !filled[g] {
			need[it.key()] = true
		}
	}
	form := strconv.Itoa(level)
	if level > 1 {
		parent := home.stack[level-2]
		if parent.cur < len(parent.items) {
			form += "/" + parent.items[parent.cur].key()
		}
	}
	if len(need) == 0 || home.needFor == form {
		home.needFor = ""
		return true
	}
	home.needFor = form
	blinkLocked(need)
	return false
}

// HomeBlink makes the item key of the middle column blink (what was typed
// in it did not do: not an address).
func HomeBlink(key string) {
	home.Lock()
	blinkLocked(map[string]bool{key: true})
	home.Unlock()
}

// blinkLocked makes the items keys blink, as a character hit in a game.
func blinkLocked(keys map[string]bool) {
	home.need, home.needAt = keys, time.Now()
	go func() { // the blinking
		t := time.NewTicker(33 * time.Millisecond)
		defer t.Stop()
		end := time.After(homeNeedTime + 50*time.Millisecond)
		for {
			select {
			case <-t.C:
				RequestRedraw()
			case <-end:
				RequestRedraw()
				return
			}
		}
	}()
}

// needGone reports whether a field to fill is in its blink's gone half now.
func needGone(key string) bool {
	el := time.Since(home.needAt)
	if !home.need[key] || el >= homeNeedTime {
		return false
	}
	return int(el/homeBlinkEvery)%2 == 0
}

// fieldKey names the chosen field: its column and key.
func fieldKeyLocked(it HomeItem) string { return strconv.Itoa(len(home.stack)) + "/" + it.key() }

// typeLocked starts typing in the chosen field: from its value.
func typeLocked(it HomeItem) {
	if k := fieldKeyLocked(it); !home.editing || home.editKey != k {
		home.editing, home.editKey, home.edit = true, k, it.Field.Value
	}
}

// setFieldLocked ends typing in the chosen field: what sets it (after the
// lock), if anything was changed.
func setFieldLocked() func() {
	if !home.editing {
		return nil
	}
	home.editing = false
	it, ok := currentLocked()
	if !ok || it.Field == nil || fieldKeyLocked(it) != home.editKey {
		return nil
	}
	v, set := strings.TrimSpace(home.edit), it.Field.Set
	if v == it.Field.Value || set == nil {
		return nil
	}
	home.pendKey, home.pendVal = home.editKey, v
	return func() { set(v) }
}

// The columns slide when one is opened (from where it was shown, to the
// right, into the middle) or closed (back out to the right): homeAnimTime
// long, slowing down, a cell at a time.
const homeAnimTime = 180 * time.Millisecond

// pushLocked opens col: the columns slide left by the way from the middle
// column's centre to col's, shown on its right.
func pushLocked(col homeColumn) {
	old := homeColumnWidth(home.stack[len(home.stack)-1].items)
	home.stack = append(home.stack, col)
	startAnimLocked(old/2 + homeGap() + homeColumnWidth(col.items)/2)
}

// popLocked closes the middle column: the columns slide right.
func popLocked() {
	if len(home.stack) < 2 {
		return
	}
	child := homeColumnWidth(home.stack[len(home.stack)-1].items)
	truncateLocked(len(home.stack) - 1)
	startAnimLocked(-(homeColumnWidth(home.stack[len(home.stack)-1].items)/2 + homeGap() + child/2))
}

// truncateLocked closes the columns past the first n, each one's item's
// Leave done.
func truncateLocked(n int) {
	for len(home.stack) > max(1, n) {
		parent := home.stack[len(home.stack)-2]
		home.stack = home.stack[:len(home.stack)-1]
		if parent.cur < len(parent.items) {
			if leave := parent.items[parent.cur].Leave; leave != nil {
				leave()
			}
		}
	}
}

// startAnimLocked starts the columns d cells off (right for d > 0) and
// redraws them as they come back, until they are in place.
func startAnimLocked(d int) {
	home.animFrom, home.animAt = d, time.Now()
	go func() {
		t := time.NewTicker(16 * time.Millisecond)
		defer t.Stop()
		end := time.After(homeAnimTime + 20*time.Millisecond)
		for {
			select {
			case <-t.C:
				RequestRedraw()
			case <-end:
				RequestRedraw()
				return
			}
		}
	}()
}

// homeShiftLocked is how far off the columns are now (cells).
func homeShiftLocked() int {
	el := time.Since(home.animAt)
	if home.animFrom == 0 || el >= homeAnimTime {
		return 0
	}
	p := float64(el) / float64(homeAnimTime)
	left := (1 - p) * (1 - p) * (1 - p) // ease out: fast, then slower
	return int(math.Round(float64(home.animFrom) * left))
}

// jumpLocked moves to the next item beginning with what was typed (letters
// typed within a second make a word).
func jumpLocked(r rune) {
	now := time.Now()
	if now.Sub(home.typedAt) > time.Second {
		home.typed = ""
	}
	home.typedAt = now
	home.typed += strings.ToLower(string(r))
	top := &home.stack[len(home.stack)-1]
	n := len(top.items)
	from := top.cur
	if len([]rune(home.typed)) == 1 {
		from++ // the same letter again: the next one
	}
	for i := 0; i < n; i++ {
		j := (from + i) % n
		if strings.HasPrefix(strings.ToLower(top.items[j].Text), home.typed) {
			top.cur = j
			return
		}
	}
}

// activateLocked does what the chosen item does.
func activateLocked() func() {
	it, ok := currentLocked()
	if !ok {
		return nil
	}
	switch {
	case (it.Back || it.Close) && !mayLeaveLocked():
	case it.Back:
		popLocked()
	case it.Close && it.CloseTo != "":
		for len(home.stack) > 1 && !hasKey(home.stack[len(home.stack)-1].items, it.CloseTo) {
			truncateLocked(len(home.stack) - 1)
		}
	case it.Close:
		if len(home.stack) > 1 {
			truncateLocked(1)
		} else if home.quit != nil {
			return home.quit
		}
	case it.Field != nil:
		typeLocked(it)
	case it.Open != nil:
		col := openColumn(it.Open())
		if it.Leaf && onlyNav(col.items) {
			return it.Enter
		}
		pushLocked(col)
		if it.Enter != nil {
			return it.Enter
		}
	case it.Do != nil:
		if it.Flash != "" {
			home.flashAt, home.flashText, home.flashTo = it.key(), "", time.Now().Add(1200*time.Millisecond)
			time.AfterFunc(1300*time.Millisecond, RequestRedraw)
		}
		return func() {
			it.Do()
			HomeRefresh()
		}
	}
	return nil
}

// onlyNav reports whether items are only < and x.
// navFrom is where a column's last items, + and < and x, begin, a line
// drawn above them; -1 when the column has none, or nothing else.
func navFrom(items []HomeItem) int {
	at := len(items)
	for at > 0 && (items[at-1].Back || items[at-1].Close || items[at-1].Bottom) {
		at--
	}
	if at == 0 || at == len(items) {
		return -1
	}
	return at
}

// homeSlot is where item i of a column is drawn, counted in rows from its
// first: the line above + and < and x takes a row in a terminal; the window
// draws it between two rows (none taken).
func homeSlot(items []HomeItem, i int) int {
	if pixelMode {
		return i
	}
	if sep := navFrom(items); sep >= 0 && i >= sep {
		return i + 1
	}
	return i
}

func onlyNav(items []HomeItem) bool {
	for _, it := range items {
		if !it.Back && !it.Close {
			return false
		}
	}
	return true
}

func hasKey(items []HomeItem, key string) bool {
	for _, it := range items {
		if it.key() == key {
			return true
		}
	}
	return false
}

// homePaste is a paste while the home screen is shown: into the field being
// edited, else an address to call.
func homePaste(s string) {
	home.Lock()
	if it, ok := currentLocked(); ok && it.Field != nil {
		typeLocked(it)
		home.edit += strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
		home.Unlock()
		RequestRedraw()
		return
	}
	paste := home.paste
	home.Unlock()
	if paste != nil {
		paste(strings.TrimSpace(s))
	}
}

// homeClick is a click while the home screen is shown: an item in the
// middle is chosen and done, one to the left goes back to its column, one
// to the right opens the chosen item at it.
func homeClick(col, row int) {
	home.Lock()
	set := setFieldLocked() // a click leaves the field
	var act func()
	for _, a := range home.clicks {
		if a.row != row || col < a.c0 || col > a.c1 {
			continue
		}
		top := len(home.stack) - 1
		switch {
		case a.level == top:
			home.stack[top].cur = a.item
			act = activateLocked()
		case a.level < top:
			if mayLeaveLocked() {
				truncateLocked(a.level + 1)
				home.stack[a.level].cur = a.item
			}
		case a.level > top:
			act = activateLocked()
			if len(home.stack) == a.level+1 {
				home.stack[a.level].cur = a.item
			}
		}
		break
	}
	home.Unlock()
	if set != nil {
		set()
	}
	if act != nil {
		act()
	}
	if set != nil || act != nil {
		HomeRefresh() // a form may have changed: built again
	}
	RequestRedraw()
}

// homePadCols and homePadRows are the blank inside a menu's frame round
// its items. The window as its call screen's status (statusPadCols): three
// cells each side; above and below it gives half a row itself
// (hud.ContentFrameOf, as round every content), none here. A terminal, whose frame's lines take cells
// of their own: two cells each side, no rows.
func homePadCols() int {
	if pixelMode {
		return statusPadCols
	}
	return 2
}

func homePadRows() int { return 0 }

// homeGap is the blank between two columns' items: their frames' padding
// and lines, a space each side of the arrow between them (│ → │).
func homeGap() int { return 5 + 2*homePadCols() }

// renderHomeScreen draws the menu.
func renderHomeScreen(cols, rows int) string {
	home.Lock()
	defer home.Unlock()
	beginScreen(screenHome, cols, rows)
	paneCache.reset()
	statusClickClearAreas()

	var out strings.Builder
	writeFramePrefix(&out, true)
	appendBorderClear(&out, cols, rows)
	if termHUD {
		h := newHUDCanvas()
		if gfxHUD {
			gfxBegin()
			gfxBorder()
		}
		hudWindowBorder(h, cols, rows)
		if gfxHUD {
			h.blank()
		}
		h.draw(outAt(&out))
	}
	home.clicks = home.clicks[:0]
	if len(home.stack) == 0 {
		return out.String()
	}
	home.shift = homeShiftLocked()
	defer func() { home.shift = 0 }()

	top, bottom := termBorder+1, rows-termBorder
	mid := (top + bottom) / 2
	centre := (cols + 1) / 2
	level := len(home.stack) - 1
	cur := home.stack[level]
	chosen, _ := currentLocked()
	tint := homeTint(chosen)
	block := homeChosen(tint)
	// The blocks of the columns around the middle one, left and right, are
	// grey, as faint text: only the middle one is what Enter does.
	muted := homeChosen(homeFaintColour)

	// Each column's chosen item is a block as wide as the column, in the
	// chosen line's colour, on the middle row; its text stays where the
	// column's other items are. Between the blocks, in the blank, an arrow.
	var blocks [][2]int // left to right
	drawBlock := func(l, r int, style string) {
		homePut(&out, mid, l, cols, strings.Repeat(" ", max(0, r-l+1)), style)
	}
	// Each column in a frame a cell out of its items' rows, first to last,
	// and its block's cells l..r (homeframe.go: drawn as the window draws
	// its frames, from the column's seed); the lines past the screen's top
	// or bottom are not drawn. The line above + and < and x joins the
	// frame's sides.
	drawFrame := func(items []HomeItem, cur, l, r int, style string, colour [3]int, seed uint64) {
		homeMenuFrame(&out, items, cur, l, r, mid, top, bottom, cols, style, colour, seed)
	}
	// The middle column, centred.
	width := homeColumnWidth(cur.items)
	left := centre - width/2
	for i, it := range cur.items {
		row := mid + homeSlot(cur.items, i) - homeSlot(cur.items, cur.cur)
		if row < top || row > bottom {
			continue
		}
		text, style := homeLine(it, i == cur.cur, true, tint)
		x := centre - displayWidth(text)/2
		if i == cur.cur {
			drawBlock(left-homePadCols(), left+width-1+homePadCols(), block)
		}
		if needGone(it.key()) {
			continue // to fill, blinking: gone this moment
		}
		homePut(&out, row, x, cols, text, style)
		putTick(&out, it, text, row, x, cols, style, i == cur.cur)
		home.clicks = append(home.clicks, homeClickArea{row, max(1, x) + home.shift, x + displayWidth(text) - 1 + home.shift, level, i})
	}
	middle := [2]int{left, left + width - 1}
	// Each frame in its chosen line's block's colour: the middle one's the
	// chosen item's (it changes with it), the side ones' grey.
	drawFrame(cur.items, cur.cur, left, left+width-1, homeFg(tint), tint, homeFrameSeed(homePath(level)))

	// The columns it was opened from, to the left: each its block homeGap()
	// blank from the next, its items centred in it, running off the left
	// edge.
	var before [][2]int
	right := left - 1 - homeGap() // the right end of the column's block
	for l := level - 1; l >= 0 && right > 1; l-- {
		col := home.stack[l]
		w := homeColumnWidth(col.items)
		edge := right - 1 // where its text ends
		for i, it := range col.items {
			row := mid + homeSlot(col.items, i) - homeSlot(col.items, col.cur)
			if row < top || row > bottom {
				continue
			}
			text, style := homeLine(it, i == col.cur, false, tint)
			if i != col.cur {
				style = homeFaint()
			} else {
				style = muted
				drawBlock(max(1, right-w+1-homePadCols()), right+homePadCols(), muted)
			}
			// Centred in its column's block, as the middle column's are (a
			// name past the left edge is cut there).
			x := right - w + 1 + (w-displayWidth(text))/2
			homePut(&out, row, x, cols, text, style)
			home.clicks = append(home.clicks, homeClickArea{row, max(1, x) + home.shift, min(edge, x+displayWidth(text)-1) + home.shift, l, i})
		}
		drawFrame(col.items, col.cur, right-w+1, right, homeFaint(), homeFaintColour, homeFrameSeed(homePath(l)))
		before = append([][2]int{{right - w + 1, right}}, before...)
		right -= w + homeGap()
	}
	blocks = append(append(blocks, before...), middle)

	// What the chosen item opens, faint, to the right (its block too).
	if !home.editing && chosen.Open != nil {
		next := openColumn(chosen.Open()) // at the item it opens at
		if chosen.Leaf && onlyNav(next.items) {
			next.items = nil // a folder without folders: nothing to show
		}
		w := homeColumnWidth(next.items)
		bl := left + width + homeGap()
		drawFrame(next.items, next.cur, bl, bl+w-1, homeFaint(), homeFaintColour, homeFrameSeed(homePath(level+1)))
		for i, it := range next.items {
			row := mid + homeSlot(next.items, i) - homeSlot(next.items, next.cur)
			text, style := homeText(it), homeFaint()
			x := bl + (w-displayWidth(text))/2 // centred in its block
			if row < top || row > bottom || x > cols {
				continue
			}
			if row == mid {
				style = muted
				drawBlock(bl-homePadCols(), min(cols, bl+w-1+homePadCols()), muted)
			}
			if displayWidth(text) > cols-x+1 {
				text = truncateWidth(text, cols-x+1)
			}
			homePut(&out, row, x, cols, text, style)
			home.clicks = append(home.clicks, homeClickArea{row, x + home.shift, x + displayWidth(text) - 1 + home.shift, level + 1, i})
		}
		if next.cur < len(next.items) && bl+1+home.shift <= cols { // not to a block off the screen
			blocks = append(blocks, [2]int{bl, bl + w - 1})
		}
	}

	// The arrows, in the blank between blocks, as the menu's text.
	for k := 0; k+1 < len(blocks); k++ {
		if at := (blocks[k][1] + blocks[k+1][0]) / 2; at > blocks[k][1] && at < blocks[k+1][0] {
			homePut(&out, mid, at, cols, "→", homeNormal())
		}
	}
	out.WriteString("\x1b[0m")
	return out.String()
}

// homeMark is the mark before an item's text, a cell and a space: ✓ the
// one chosen of its kind (as the call's menu marks what is on), ● a person
// online; "" none.
func homeMark(it HomeItem) string {
	switch {
	case it.Mark != "":
		return it.Mark
	case it.Tick:
		return "✓"
	case it.Online:
		return "●"
	}
	return ""
}

// homeMarkColour is the marks' green (the call's menu's).
var homeMarkColour = [3]int{40, 200, 70}

// homeMenuFrame frames a menu's column (the home screen's, a call view's):
// its items' rows, the chosen one (cur) on row mid, and its block's cells
// l..r, with the padding round them (homePadCols, homePadRows); a
// terminal draws it in lines from seed
// (homeframe.go), the window as its panes' (OSC 5581), the line above +
// and < and x with it. Rows past top and bottom are not drawn.
func homeMenuFrame(out *strings.Builder, items []HomeItem, cur, l, r, mid, top, bottom, cols int, style string, colour [3]int, seed uint64) {
	n := len(items)
	if n == 0 {
		return
	}
	fs := frameLinesOf(seed, false, false)
	first := mid - homeSlot(items, cur)
	last := first + homeSlot(items, n-1)
	// The frame round the items and their padding.
	l, r, first, last = l-homePadCols(), r+homePadCols(), first-homePadRows(), last+homePadRows()
	w := max(0, r-l+1)
	if pixelMode {
		// The window draws the frame itself, as its panes' (OSC 5581),
		// the line above + and < and x with it.
		divider := 0
		if sep := navFrom(items); sep >= 0 {
			divider = homePadRows() + sep + 1
		}
		writePixelMenu(out, first, l+home.shift, w, last-first+1, seed, colour, divider)
		return
	}
	if first-1 >= top {
		homePut(out, first-1, l-1, cols, string(fs.corner(0))+fs.hLine(0, w)+string(fs.corner(1)), style)
	}
	if last+1 <= bottom {
		homePut(out, last+1, l-1, cols, string(fs.corner(3))+fs.hLine(2, w)+string(fs.corner(2)), style)
	}
	sepRow := top - 1
	if sep := navFrom(items); sep >= 0 {
		sepRow = first + homePadRows() + sep
	}
	h := float64(last - first + 1)
	for row := max(top, first); row <= min(bottom, last); row++ {
		at := (float64(row-first) + 0.5) / h
		lw, rw := fs.weight(3, at), fs.weight(1, at)
		if row == sepRow {
			homePut(out, row, l-1, cols, string(leftTs[lw])+strings.Repeat("─", w)+string(rightTs[rw]), style)
			continue
		}
		homePut(out, row, l-1, cols, string(vLines[lw]), style)
		homePut(out, row, r+1, cols, string(vLines[rw]), style)
	}
}

// homeText is an item's text as shown when not chosen (its mark before it).
func homeText(it HomeItem) string {
	if m := homeMark(it); m != "" {
		return m + " " + homeTextPlain(it)
	}
	return homeTextPlain(it)
}

// homeCloseColour is x's red in every menu (it closes, hangs up).
var homeCloseColour = [3]int{230, 40, 40}

// putTick colours the mark of an item of the active menu (the middle
// column, a call view's) drawn at row, x with style (its block's background
// kept): green; and x, red. The side columns' marks are as grey as their
// text.
// The chosen item's marks (on its block) are its text's colour.
func putTick(out *strings.Builder, it HomeItem, text string, row, x, cols int, style string, chosen bool) {
	put := func(m string, c [3]int) {
		if chosen {
			return
		}
		homePut(out, row, x, cols, m, style+homeFg(c))
	}
	if it.Close && text == "x" {
		put("x", homeCloseColour)
		return
	}
	if m := homeMark(it); m != "" && strings.HasPrefix(text, m+" ") {
		c := homeMarkColour
		if it.Mark != "" {
			c = it.MarkColour
		}
		put(m, c)
	}
}

func homeTextPlain(it HomeItem) string {
	switch {
	case it.Back:
		return "<"
	case it.Close:
		return "x"
	case home.flashAt != "" && it.key() == home.flashAt && time.Now().Before(home.flashTo):
		if home.flashText != "" {
			return home.flashText
		}
		return it.Flash
	case it.Field != nil && it.Field.Value == "":
		return it.Field.Placeholder
	case it.Field != nil:
		return it.Field.Value + it.Field.Suffix
	}
	return it.Text
}

// homeLine is an item's text and style: chosen in the middle column it is
// on its colour, a space either side (a field being edited with its cursor).
func homeLine(it HomeItem, chosen, middle bool, tint [3]int) (string, string) {
	text := homeText(it)
	if chosen && middle && it.Field != nil && home.editing && home.editKey == fieldKeyLocked(it) {
		return home.edit + "_", homeChosen(tint)
	}
	if chosen {
		return text, homeChosen(tint) // on its block: where the others are
	}
	if middle && framed() {
		// The active menu's text is its frame's colour inverted
		// (BrickColour), as the status's in its frame; a field's
		// placeholder too: it reads as a menu item.
		return text, homeFg(BrickColour(tint))
	}
	return text, homeNormal()
}

// homeColumnWidth is the widest item of a column (the padding is round it).
func homeColumnWidth(items []HomeItem) int {
	w := 0
	for _, it := range items {
		w = max(w, displayWidth(homeText(it)))
	}
	if home.editing {
		w = max(w, displayWidth(home.edit)+1) // its cursor
	}
	return w
}

// homeTint is the chosen item's colour: its own, else ours.
func homeTint(it HomeItem) [3]int {
	if it.Tinted {
		return it.Colour
	}
	self, _ := Accents()
	return self
}

// AddressColour is the colour Say gives an address (its frame in a call).
func AddressColour(addr string) [3]int { return accentPresets[addressColour(addr)] }

func homePut(out *strings.Builder, row, col, cols int, text, style string) {
	text = strings.Map(func(r rune) rune { // a name from a card is text, never a terminal's sequence
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, text)
	col += home.shift // the columns sliding (the lock is held)
	if col > cols || text == "" {
		return
	}
	if col < 1 {
		text = tailWidth(text, displayWidth(text)+col-1)
		col = 1
	}
	if displayWidth(text) > cols-col+1 {
		text = truncateWidth(text, cols-col+1)
	}
	fmt.Fprintf(out, "\x1b[%d;%dH\x1b[0m%s%s\x1b[0m", row, col, style, text)
}

// homeNormal, homeFaint and homeChosen are the styles of a line.
func homeNormal() string {
	if !framed() {
		return ""
	}
	return homeFg(homeNormalColour)
}

// homeNormalColour is the menu's text colour.
var homeNormalColour = [3]int{215, 215, 215}

// homeFaintColour is faint text's colour (and the side columns' blocks').
var homeFaintColour = [3]int{70, 70, 76}

func homeFaint() string {
	if !framed() {
		return "\x1b[2m"
	}
	return homeFg(homeFaintColour)
}

// homeChosen is the chosen line: on its colour, in black on a light one
// (yellow, cyan, green) and in white on a dark one.
func homeChosen(c [3]int) string {
	if ansiColors() {
		p := ansiPalette[nearestPlain(c[0], c[1], c[2], 16)]
		if p := ansiPalette[nearestPlain(c[0], c[1], c[2], 8)]; luma(p) > homeLightBg {
			return "\x1b[" + string(appendANSIBg(nil, p)) + ";22;30m"
		}
		return "\x1b[" + string(appendANSIBg(nil, p)) + ";1;37m"
	}
	if luma(c) > homeLightBg {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm\x1b[1m\x1b[38;2;0;0;0m", c[0], c[1], c[2])
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm\x1b[1m\x1b[38;2;255;255;255m", c[0], c[1], c[2])
}

// homeLightBg is the luma (luma's scale, 0..255000) above which a colour
// is light: white text on it does not read.
const homeLightBg = 145000

func homeFg(c [3]int) string {
	if ansiColors() {
		p := ansiPalette[nearestPlain(c[0], c[1], c[2], 16)]
		return "\x1b[" + string(appendANSIFg(nil, p)) + "m"
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
}

var (
	dialCancel atomicFunc // Esc while a call is dialled
	hangupFunc atomicFunc // the self view's menu: hang up
)

type atomicFunc struct {
	mu sync.Mutex
	f  func()
}

func (a *atomicFunc) set(f func()) { a.mu.Lock(); a.f = f; a.mu.Unlock() }
func (a *atomicFunc) get() func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f
}

// SetDialCancel is what Esc does while a call is dialled (nil: nothing).
func SetDialCancel(f func()) { dialCancel.set(f) }

func cancelDial() {
	if f := dialCancel.get(); f != nil {
		go f()
	}
}

// SetHangup is what the self view's menu item Hang up does (nil: no item).
func SetHangup(f func()) { hangupFunc.set(f) }
