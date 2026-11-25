package ui

import (
	"fmt"
	"slices"
	"strings"
)

// The window's frames in a terminal, drawn with lines: the layout of the
// window (wider margins, a frame round each video pane and round the chat,
// the captions on one bracketed line with the ping between them, a border
// round the screen), each frame a box of box-drawing lines in the
// window's colours, as the window's frame from the same seed: its cut
// corners rounded or square, its plates heavy lines, its stripes dashed,
// its rails double (homeframe.go). The window's shapes themselves in
// blocks and quarter blocks broke up into steps in a terminal's font; they
// stay the window's and the HUD image's (graphics.go). The whole screen is
// black, as the window is. Only with 24-bit colour: the accents are the
// window's.
var termHUD bool

// framed reports whether the interface has the window's frames: in the
// window, or in a terminal that draws them (termHUD).
func framed() bool { return pixelMode || termHUD }

// setTermHUD turns the terminal's frames on or off, with the margins they
// need. Call it before the renderer starts.
func setTermHUD(on bool) {
	if pixelMode {
		return
	}
	termHUD = on
	if on {
		// A row less under the captions than the window has: the terminal
		// shows no glow there, and two blank rows read as a gap.
		termBorder, termSeparator = 2, 2
		termSide, termGap = 4, 4 // the frames' lines a column out of the panes: two blank between
	} else {
		termBorder, termSeparator = 1, 1
		termSide, termGap = 1, 1
	}
}

var (
	hudOrange = [3]int{255, 140, 0}
	hudFg     = [3]int{0xd0, 0xd0, 0xd0} // the window's text colour
)

const hudBlack = "\x1b[48;2;0;0;0m"

// hudReset is a reset to the window's colours: light text on black.
const hudReset = "\x1b[0;38;2;208;208;208;48;2;0;0;0m"

var hudColours = strings.NewReplacer(
	"\x1b[0m", hudReset,
	"\x1b[49m", hudBlack,
	"\x1b[39m", "\x1b[38;2;208;208;208m",
)

// onBlack puts a frame for the terminal on the window's black: whatever
// would fall back to the terminal's own colours (resets, default colours,
// what a clear leaves) gets the window's instead.
func onBlack(data string) string {
	return hudReset + hudColours.Replace(data)
}

// cellAt positions the cursor at a 1-based row and column and returns where
// that row's text goes.
type cellAt func(row, col int) *strings.Builder

// hudCell is a cell of the frames: a character in a colour, on black or
// on a colour of its own.
type hudCell struct {
	r      rune
	fg, bg [3]int
	onBg   bool // on bg rather than black
}

// hudCanvas collects the frames' cells (by 1-based row and column) before
// they are written: the panes' cut corners are known before the video is
// (which must not be drawn there), and the frames go over the rows last.
type hudCanvas struct {
	cells map[[2]int]hudCell
}

func newHUDCanvas() *hudCanvas { return &hudCanvas{cells: map[[2]int]hudCell{}} }

func (h *hudCanvas) set(row, col int, r rune, fg [3]int) {
	h.cells[[2]int{row, col}] = hudCell{r: r, fg: fg}
}

// setOn sets a cell drawn on a background colour.
func (h *hudCanvas) setOn(row, col int, r rune, fg, bg [3]int) {
	h.cells[[2]int{row, col}] = hudCell{r: r, fg: fg, bg: bg, onBg: true}
}

// sgr is the cell's colours as an SGR sequence.
func (c hudCell) sgr() string {
	bg := hudBlack
	if c.onBg {
		bg = fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c.bg[0], c.bg[1], c.bg[2])
	}
	return bg + accentSGR(c.fg)
}

// has reports whether the frames take the cell: the video leaves it.
func (h *hudCanvas) has(row, col int) bool {
	if h == nil {
		return false
	}
	_, ok := h.cells[[2]int{row, col}]
	return ok
}

// draw writes the cells, a run of neighbours at a time.
func (h *hudCanvas) draw(at cellAt) {
	keys := make([][2]int, 0, len(h.cells))
	for k := range h.cells {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]int) int {
		if a[0] != b[0] {
			return a[0] - b[0]
		}
		return a[1] - b[1]
	})
	var b *strings.Builder
	last := [2]int{-1, -1}
	var colours string
	for _, k := range keys {
		c := h.cells[k]
		if k[0] != last[0] || k[1] != last[1]+1 {
			if b != nil {
				b.WriteString("\x1b[0m")
			}
			b = at(k[0], k[1])
			colours = c.sgr()
			b.WriteString(colours)
		} else if sgr := c.sgr(); sgr != colours {
			b.WriteString(sgr)
			colours = sgr
		}
		b.WriteRune(c.r)
		last = k
	}
	if b != nil {
		b.WriteString("\x1b[0m")
	}
}

// quarters: the block character with the lit quarters of a cell, by bits
// 1 top left, 2 top right, 4 bottom left, 8 bottom right.
var quarters = [16]rune{' ', '▘', '▝', '▀', '▖', '▌', '▞', '▛', '▗', '▚', '▐', '▜', '▄', '▙', '▟', '█'}

// hazardCell is the cell at (row, col) of a field of hazard stripes, in
// quarter cells: stripes two quarters wide, a quarter further left on each
// quarter row down, so that they slant as the window's do.
func hazardCell(row, col int) rune {
	q := 0
	for i, d := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		x, y := 2*col+d[0], 2*row+d[1]
		if ((x+y)/4)%2 == 0 {
			q |= 1 << i
		}
	}
	return quarters[q]
}

// mirrored is a character flipped left to right.
func mirrored(r rune) rune {
	switch r {
	case '▘', '▝', '▖', '▗', '▛', '▜', '▙', '▟', '▌', '▐', '▚', '▞':
		return flipQuarters(r, true)
	case '╱':
		return '╲'
	case '╲':
		return '╱'
	}
	return r
}

// flipped is a character flipped top to bottom.
func flipped(r rune) rune {
	switch r {
	case '▘', '▝', '▖', '▗', '▛', '▜', '▙', '▟', '▀', '▄', '▚', '▞':
		return flipQuarters(r, false)
	}
	return r
}

// flipQuarters flips a quarter block left to right (across) or top to
// bottom.
func flipQuarters(r rune, across bool) rune {
	q := 0
	for i, c := range quarters {
		if c == r {
			q = i
		}
	}
	var f int
	for i := range 4 {
		if q&(1<<i) == 0 {
			continue
		}
		j := i ^ 2 // top <-> bottom
		if across {
			j = i ^ 1 // left <-> right
		}
		f |= 1 << j
	}
	return quarters[f]
}

// hudWindowBorder draws the screen's border round the whole screen in
// orange, as the window draws its own (borderLines).
func hudWindowBorder(h *hudCanvas, cols, rows int) {
	if cols < 12 || rows < 6 {
		return
	}
	self, _, _ := FrameSeeds() // the window's border is from ours too
	borderLines(self).draw(h, 1, 1, cols, rows, hudOrange)
}

// hudBox frames the rect of cells top, left, cols x rows (its outermost
// cells are the frame) in colour c, as the window frames it from seed
// (frameLines: mirrored on the peer's pane, the chat's with its small
// cuts).
func hudBox(h *hudCanvas, top, left, cols, rows int, c [3]int, seed uint64, mirror, chat bool) {
	if cols < 6 || rows < 4 {
		return
	}
	frameLinesOf(seed, chat, mirror).draw(h, top, left, cols, rows, c)
}

// hudBracket draws a bracketed row in orange, as the window draws its
// marks (writePixelBracket): a half block at each end of the row (1-based
// row and column, width cells) and a line between the pieces of text, which
// span [from, to) of the row's columns, a cell clear of each.
func hudBracket(b *strings.Builder, row, left, width int, spans ...[2]int) {
	if width < 4 {
		return
	}
	line := make([]bool, width)
	for i := 1; i < width-1; i++ {
		line[i] = true
	}
	for _, sp := range spans {
		if sp[0] >= sp[1] || sp[0] < 1 || sp[1] > width-1 {
			continue
		}
		for i := max(1, sp[0]-1); i < min(width-1, sp[1]+1); i++ {
			line[i] = false
		}
	}
	end, mid := "├", "─"
	if gfxHUD { // drawn in the image: the cells stay blank
		gfxBracket(row, left, width, spans...)
		end, mid = " ", " "
	}
	writeCursorTo(b, row, left)
	b.WriteString(hudBlack + accentSGR(hudOrange) + end)
	for i := 1; i < width-1; i++ {
		if !line[i] {
			continue
		}
		j := i
		for j < width-1 && line[j] {
			j++
		}
		writeCursorTo(b, row, left+i)
		b.WriteString(strings.Repeat(mid, j-i))
		i = j - 1
	}
	writeCursorTo(b, row, left+width-1)
	if end == "├" {
		end = "┤"
	}
	b.WriteString(end + "\x1b[0m")
}

// blank turns every cell of the canvas into a blank one on black: the
// frames' cells when the frames go as an image.
func (h *hudCanvas) blank() {
	for k := range h.cells {
		h.cells[k] = hudCell{r: ' ', fg: hudFg}
	}
}

// tee turns the frame's side at row, col (its left side, or its right) into
// a T for a thin line meeting it from inside (├ ┤ by the side's weight), in
// colour c.
func (h *hudCanvas) tee(row, col int, leftSide bool, c [3]int) {
	cell, ok := h.cells[[2]int{row, col}]
	if !ok {
		return
	}
	for w, v := range vLines {
		if cell.r == v {
			t := rightTs[w]
			if leftSide {
				t = leftTs[w]
			}
			h.set(row, col, t, c)
			return
		}
	}
}

// hudVideoMask is the frames' canvas while the panes are drawn: the cells
// it takes (the cut corners) are left out of the video.
var hudVideoMask *hudCanvas
