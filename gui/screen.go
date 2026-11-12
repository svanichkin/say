package gui

import (
	"image/color"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/svanichkin/say/device"
)

// The window is a terminal for the interface: it draws into it exactly as
// into a real one, and Screen keeps what it drew as a model the window
// renders, cell by cell:
//
//   - a cell has a background and either a character of text (in a
//     monospaced system font: one cell, two for wide characters) or, for box
//     drawing and blocks, a 4x8 shape drawn on the cell (cells.go);
//   - video panes are rectangles of cells showing a frame, marked by the
//     interface (OSC 5577) and drawn at the frame's own resolution.
//
// As with a terminal, resizing the window changes the number of cells.

// Cols and Rows are the default size: two panes 44 cells wide side by side,
// with the window's margins (2 cells round, 4 between: ui.SetPixelMode), and
// about as much again below for the chat.
const (
	Cols = 96
	Rows = 45
)

var (
	defaultFg = color.RGBA{0xd0, 0xd0, 0xd0, 0xff}
	defaultBg = color.RGBA{0x00, 0x00, 0x00, 0xff}
)

// Cell is one cell of the screen.
type Cell struct {
	Bg      color.RGBA
	Fg      color.RGBA
	Text    string // the character (with its combining marks), if any
	Wide    bool   // Text takes this cell and the next
	Pattern uint32 // the shape on the cell, if Shape
	Shape   bool
	Video   bool // a video pane shows here
}

// Pane is a video pane: its cells, the frame to show (an id the interface
// hands frames out by) and whether it is the peer's.
type Pane struct {
	Row, Col, Cols, Rows int
	Frame                uint64
	Remote               bool
	Caption              string // the frame rate, the traffic, the ping
	Chat                 bool   // not video: the chat, framed in its own style
}

// Screen is the model of the window's contents, written as a terminal.
type Screen struct {
	mu         sync.Mutex
	cols, rows int
	cells      []Cell
	panes      []Pane
	captions   map[[2]int]string  // by the pane's row and column
	brackets   map[[2]int]Bracket // by the caption's row and column
	version    uint64             // grows with every change
	changed    chan struct{}

	// Parser state.
	state  int
	params []int
	num    int
	hasNum bool
	priv   bool
	utf    []byte
	osc    []byte

	// Cursor, in cells; last is the cell of the last character (for
	// combining marks), -1 after a move.
	row, col int
	last     int
	fg, bg   color.RGBA
	reverse  bool
}

const (
	stGround = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
)

// NewScreen returns a cleared screen of cols x rows cells.
func NewScreen(cols, rows int) *Screen {
	s := &Screen{fg: defaultFg, bg: defaultBg, changed: make(chan struct{}, 1), last: -1}
	s.Resize(cols, rows)
	return s
}

// Resize gives the screen a new size; it reports whether the size changed.
// What fits is kept: while the window is dragged, macOS lets it draw only as
// the size changes, before the interface has redrawn for the new size, and a
// cleared screen would show blank until the mouse is let go.
func (s *Screen) Resize(cols, rows int) bool {
	cols, rows = max(1, cols), max(1, rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cells != nil && cols == s.cols && rows == s.rows {
		return false
	}
	cells := make([]Cell, cols*rows)
	for i := range cells {
		cells[i] = Cell{Bg: defaultBg, Fg: defaultFg}
	}
	for r := 0; r < min(rows, s.rows); r++ {
		copy(cells[r*cols:r*cols+min(cols, s.cols)], s.cells[r*s.cols:r*s.cols+min(cols, s.cols)])
	}
	s.cols, s.rows, s.cells = cols, rows, cells
	s.last = -1
	s.version++
	return true
}

// Size returns the screen's size in cells.
func (s *Screen) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Changed delivers a value after the screen changed (one value stands for
// any number of changes until it is taken).
func (s *Screen) Changed() <-chan struct{} { return s.changed }

// View hands the model to fn under the screen's lock; fn must not keep the
// slices.
func (s *Screen) View(fn func(cols, rows int, cells []Cell, panes []Pane, version uint64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.cols, s.rows, s.cells, s.panes, s.version)
}

// Write parses terminal output into the model.
func (s *Screen) Write(p []byte) (int, error) {
	acks := 0
	s.mu.Lock()
	before := s.version
	for _, b := range p {
		acks += s.feed(b)
	}
	changed := s.version != before
	s.mu.Unlock()
	if changed {
		select {
		case s.changed <- struct{}{}:
		default:
		}
	}
	for range acks { // ESC[5n after a frame: the model has it
		device.TerminalAck()
	}
	return len(p), nil
}

func (s *Screen) feed(b byte) (acks int) {
	switch s.state {
	case stEsc:
		switch b {
		case '[':
			s.state, s.params, s.num, s.hasNum, s.priv = stCSI, s.params[:0], 0, false, false
		case ']':
			s.state, s.osc = stOSC, s.osc[:0]
		default:
			s.state = stGround
		}
		return 0
	case stCSI:
		switch {
		case b >= '0' && b <= '9':
			s.num = s.num*10 + int(b-'0')
			s.hasNum = true
		case b == ';':
			s.params = append(s.params, s.numOr(-1))
			s.num, s.hasNum = 0, false
		case b == '?' || b == '>' || b == '=':
			s.priv = true
		case b >= 0x40 && b <= 0x7e:
			if s.hasNum || len(s.params) > 0 {
				s.params = append(s.params, s.numOr(-1))
			}
			s.state = stGround
			return s.csi(b)
		}
		return 0
	case stOSC:
		switch b {
		case 0x07:
			s.state = stGround
			s.oscDone()
		case 0x1b:
			s.state = stOSCEsc
		default:
			if len(s.osc) < 256 {
				s.osc = append(s.osc, b)
			}
		}
		return 0
	case stOSCEsc:
		s.state = stGround
		s.oscDone()
		return 0
	}
	if len(s.utf) > 0 || b >= 0x80 {
		s.utf = append(s.utf, b)
		if !utf8.FullRune(s.utf) {
			return 0
		}
		r, _ := utf8.DecodeRune(s.utf)
		s.utf = s.utf[:0]
		s.put(r)
		return 0
	}
	switch b {
	case 0x1b:
		s.state = stEsc
	case '\r':
		s.moveTo(s.row, 0)
	case '\n':
		s.moveTo(s.row+1, s.col)
	case '\b':
		s.moveTo(s.row, max(0, s.col-1))
	case '\t':
		s.moveTo(s.row, (s.col/8+1)*8)
	default:
		if b >= 0x20 {
			s.put(rune(b))
		}
	}
	return 0
}

func (s *Screen) numOr(def int) int {
	if s.hasNum {
		return s.num
	}
	return def
}

func (s *Screen) param(i, def int) int {
	if i < len(s.params) && s.params[i] >= 0 {
		return s.params[i]
	}
	return def
}

func (s *Screen) moveTo(row, col int) {
	s.row, s.col, s.last = row, col, -1
}

func (s *Screen) csi(final byte) (acks int) {
	if s.priv {
		return 0 // modes (cursor, mouse, sync output): nothing to draw
	}
	switch final {
	case 'H', 'f':
		s.moveTo(s.param(0, 1)-1, s.param(1, 1)-1)
	case 'A':
		s.moveTo(max(0, s.row-s.param(0, 1)), s.col)
	case 'B':
		s.moveTo(s.row+s.param(0, 1), s.col)
	case 'C':
		s.moveTo(s.row, s.col+s.param(0, 1))
	case 'D':
		s.moveTo(s.row, max(0, s.col-s.param(0, 1)))
	case 'G':
		s.moveTo(s.row, s.param(0, 1)-1)
	case 'd':
		s.moveTo(s.param(0, 1)-1, s.col)
	case 'J':
		switch s.param(0, 0) {
		case 2, 3:
			s.clear(0, 0, s.cols, s.rows)
			s.panes = s.panes[:0]
			clear(s.brackets)
		case 0:
			s.clear(s.col, s.row, s.cols, s.row+1)
			s.clear(0, s.row+1, s.cols, s.rows)
		}
	case 'K':
		switch s.param(0, 0) {
		case 0:
			s.clear(s.col, s.row, s.cols, s.row+1)
		case 1:
			s.clear(0, s.row, s.col+1, s.row+1)
		case 2:
			s.clear(0, s.row, s.cols, s.row+1)
		}
	case 'X':
		s.clear(s.col, s.row, s.col+s.param(0, 1), s.row+1)
	case 'm':
		s.sgr()
	case 'n':
		if s.param(0, 0) == 5 {
			return 1
		}
	}
	return 0
}

// oscDone handles an OSC: 5577 marks a video pane (1-based top, left, then
// cols, rows, frame id and L or R, ours or the peer's); the others (titles,
// clipboard) draw nothing.
func (s *Screen) oscDone() {
	if strings.HasPrefix(string(s.osc), "5578;") {
		s.caption()
		return
	}
	if strings.HasPrefix(string(s.osc), "5579;") {
		s.chatMark()
		return
	}
	if strings.HasPrefix(string(s.osc), "5580;") {
		s.bracketMark()
		return
	}
	f := strings.Split(string(s.osc), ";")
	if len(f) < 6 || len(f) > 7 || f[0] != "5577" {
		return
	}
	remote := len(f) == 7 && f[6] == "R"
	f = f[:6]
	var v [5]int
	for i := range v {
		n, err := strconv.ParseUint(f[i+1], 10, 63)
		if err != nil {
			return
		}
		v[i] = int(n)
	}
	p := Pane{Row: v[0] - 1, Col: v[1] - 1, Cols: v[2], Rows: v[3], Frame: uint64(v[4]), Remote: remote}
	p.Caption = s.captions[[2]int{p.Row, p.Col}]
	// A pane replaces the panes it overlaps.
	kept := s.panes[:0]
	for _, q := range s.panes {
		if q.Row >= p.Row+p.Rows || p.Row >= q.Row+q.Rows || q.Col >= p.Col+p.Cols || p.Col >= q.Col+q.Cols {
			kept = append(kept, q)
		}
	}
	s.panes = append(kept, p)
	for r := max(0, p.Row); r < min(s.rows, p.Row+p.Rows); r++ {
		for c := max(0, p.Col); c < min(s.cols, p.Col+p.Cols); c++ {
			s.cells[r*s.cols+c] = Cell{Bg: defaultBg, Video: true}
		}
	}
	s.last = -1
	s.version++
}

// chatMark handles OSC 5579: the chat's rect (1-based top, left, then
// cols, rows), to be framed. Its cells stay as they are.
func (s *Screen) chatMark() {
	f := strings.Split(string(s.osc), ";")
	if len(f) != 5 {
		return
	}
	var v [4]int
	for i := range v {
		n, err := strconv.Atoi(f[i+1])
		if err != nil {
			return
		}
		v[i] = n
	}
	p := Pane{Row: v[0] - 1, Col: v[1] - 1, Cols: v[2], Rows: v[3], Chat: true}
	for i, q := range s.panes {
		if q.Chat {
			if q == p {
				return
			}
			s.panes[i] = p
			s.version++
			return
		}
	}
	s.panes = append(s.panes, p)
	s.version++
}

// Bracket is a row bracketed in the window: a block at each end and a
// line between the pieces of text, which span columns [from, to) of the
// row's Cols. The video captions' row is one: both captions and the ping
// between them, on one line.
type Bracket struct {
	Row, Col, Cols int
	Spans          [][2]int
}

func (b Bracket) equal(o Bracket) bool {
	return b.Row == o.Row && b.Col == o.Col && b.Cols == o.Cols && slices.Equal(b.Spans, o.Spans)
}

// bracketMark handles OSC 5580: a row to bracket (1-based row and column,
// its width, then where each piece of text starts and ends in it).
func (s *Screen) bracketMark() {
	f := strings.Split(string(s.osc), ";")
	if len(f) < 6 || len(f)%2 != 0 {
		return
	}
	v := make([]int, len(f)-1)
	for i := range v {
		n, err := strconv.Atoi(f[i+1])
		if err != nil {
			return
		}
		v[i] = n
	}
	b := Bracket{Row: v[0] - 1, Col: v[1] - 1, Cols: v[2]}
	for i := 3; i+1 < len(v); i += 2 {
		b.Spans = append(b.Spans, [2]int{v[i], v[i+1]})
	}
	key := [2]int{b.Row, b.Col}
	if s.brackets == nil {
		s.brackets = map[[2]int]Bracket{}
	}
	if s.brackets[key].equal(b) {
		return
	}
	s.brackets[key] = b
	s.version++
}

// caption handles OSC 5578: the caption of the pane at a 1-based row and
// column, shown in the pane's frame.
func (s *Screen) caption() {
	f := strings.SplitN(string(s.osc), ";", 4)
	if len(f) != 4 {
		return
	}
	row, err1 := strconv.Atoi(f[1])
	col, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil {
		return
	}
	key := [2]int{row - 1, col - 1}
	if s.captions == nil {
		s.captions = map[[2]int]string{}
	}
	if s.captions[key] == f[3] {
		return
	}
	s.captions[key] = f[3]
	for i := range s.panes {
		if s.panes[i].Row == key[0] && s.panes[i].Col == key[1] {
			s.panes[i].Caption = f[3]
		}
	}
	s.version++
}

// clear blanks cells [c0, c1) x [r0, r1) in the current background.
func (s *Screen) clear(c0, r0, c1, r1 int) {
	_, bg := s.colors()
	for r := max(0, r0); r < min(s.rows, r1); r++ {
		for c := max(0, c0); c < min(s.cols, c1); c++ {
			s.cells[r*s.cols+c] = Cell{Bg: bg, Fg: bg}
		}
	}
	s.last = -1
	s.version++
}

var ansi16 = [16]color.RGBA{
	{0, 0, 0, 255}, {0xaa, 0, 0, 255}, {0, 0xaa, 0, 255}, {0xaa, 0x55, 0, 255},
	{0, 0, 0xaa, 255}, {0xaa, 0, 0xaa, 255}, {0, 0xaa, 0xaa, 255}, {0xaa, 0xaa, 0xaa, 255},
	{0x55, 0x55, 0x55, 255}, {0xff, 0x55, 0x55, 255}, {0x55, 0xff, 0x55, 255}, {0xff, 0xff, 0x55, 255},
	{0x55, 0x55, 0xff, 255}, {0xff, 0x55, 0xff, 255}, {0x55, 0xff, 0xff, 255}, {0xff, 0xff, 0xff, 255},
}

func (s *Screen) sgr() {
	if len(s.params) == 0 {
		s.fg, s.bg, s.reverse = defaultFg, defaultBg, false
		return
	}
	for i := 0; i < len(s.params); i++ {
		p := max(0, s.params[i])
		switch {
		case p == 0:
			s.fg, s.bg, s.reverse = defaultFg, defaultBg, false
		case p == 7:
			s.reverse = true
		case p == 27:
			s.reverse = false
		case p >= 30 && p <= 37:
			s.fg = ansi16[p-30]
		case p >= 90 && p <= 97:
			s.fg = ansi16[p-90+8]
		case p >= 40 && p <= 47:
			s.bg = ansi16[p-40]
		case p >= 100 && p <= 107:
			s.bg = ansi16[p-100+8]
		case p == 39:
			s.fg = defaultFg
		case p == 49:
			s.bg = defaultBg
		case p == 38 || p == 48:
			var c color.RGBA
			switch {
			case i+4 < len(s.params) && s.params[i+1] == 2:
				c = color.RGBA{byte(s.params[i+2]), byte(s.params[i+3]), byte(s.params[i+4]), 255}
				i += 4
			case i+2 < len(s.params) && s.params[i+1] == 5:
				c = xterm256(s.params[i+2])
				i += 2
			default:
				continue
			}
			if p == 38 {
				s.fg = c
			} else {
				s.bg = c
			}
		}
	}
}

func xterm256(n int) color.RGBA {
	switch {
	case n < 16:
		return ansi16[max(0, n)]
	case n < 232:
		n -= 16
		lv := func(v int) byte {
			if v == 0 {
				return 0
			}
			return byte(55 + 40*v)
		}
		return color.RGBA{lv(n / 36), lv(n / 6 % 6), lv(n % 6), 255}
	default:
		v := byte(8 + 10*(n-232))
		return color.RGBA{v, v, v, 255}
	}
}

func (s *Screen) colors() (fg, bg color.RGBA) {
	if s.reverse {
		return s.bg, s.fg
	}
	return s.fg, s.bg
}

// put writes one character at the cursor and moves past it.
func (s *Screen) put(r rune) {
	if s.row < 0 || s.row >= s.rows {
		return
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f) {
		if s.last >= 0 { // a combining mark joins the last character
			s.cells[s.last].Text += string(r)
			s.version++
		}
		return
	}
	fg, bg := s.colors()
	if s.col >= s.cols {
		return // past the right edge: autowrap is off
	}
	i := s.row*s.cols + s.col
	s.last = i
	s.version++
	if p, ok := cellPattern(r); ok {
		s.cells[i] = Cell{Bg: bg, Fg: fg, Pattern: p, Shape: true}
		s.col++
		return
	}
	w := max(1, uniseg.StringWidth(string(r)))
	text := string(r)
	if r == ' ' {
		text = ""
	}
	s.cells[i] = Cell{Bg: bg, Fg: fg, Text: text, Wide: w == 2}
	if w == 2 && s.col+1 < s.cols {
		s.cells[i+1] = Cell{Bg: bg, Fg: fg}
	}
	s.col += w
}
