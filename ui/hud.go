package ui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// The window's frames in a terminal, drawn with block characters: the
// layout of the window (wider margins, a frame round each video pane and
// round the chat, the captions on one bracketed line with the ping between
// them, hazard bars on the window's sides) where the window draws them
// itself from marks. Everything is drawn with what terminals draw
// themselves, filling the cell, whatever the font: blocks and quarter
// blocks (the slants of cut corners, plates and hazard stripes are steps
// of quarters) and the box-drawing diagonals ╱╲ of the side bars. Glyphs
// that come from the font, such as the triangles ◢◣◤◥, are a font's size,
// smaller than a cell, and break up. The whole screen is black, as the
// window is. Only with 24-bit colour: the accents are the window's.

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
		termBorder, termSeparator = 2, 3
		termSide, termGap = 4, 6
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

// hudWindowBorder draws the window's own border: down each side a bar
// two columns wide, orange crossed by black diagonals (╱ on the left, ╲ on
// the right: the window's hazard bars), its outer corners cut and its inner
// edge stepping back over the middle (30% to 70%) of the height; along the
// top and the bottom a thin orange line between the bars with a few plates
// on it.
func hudWindowBorder(h *hudCanvas, cols, rows int) {
	if cols < 12 || rows < 6 {
		return
	}
	s1, s2 := 1+(rows-1)*3/10, 1+(rows-1)*7/10
	bar := func(r, c int) rune { // the left bar's cell (c 0 outer, 1 inner)
		switch {
		case c == 0 && (r == 1 || r == rows):
			return ' '
		case c == 1 && r > s1 && r < s2:
			return ' '
		}
		return '╱'
	}
	for r := 1; r <= rows; r++ {
		for c := range 2 {
			ch := bar(r, c)
			if ch == ' ' { // cut away: the window's black
				h.set(r, 1+c, ch, hudOrange)
				h.set(r, cols-c, ch, hudOrange)
				continue
			}
			h.setOn(r, 1+c, ch, [3]int{}, hudOrange)
			h.setOn(r, cols-c, mirrored(ch), [3]int{}, hudOrange)
		}
	}
	seed, _, _ := FrameSeeds()
	rng := rand.New(rand.NewPCG(seed, 0xb0d3))
	inner := cols - 4
	for k, row := range []int{1, rows} {
		thin, plate := '▔', '▀'
		if k == 1 {
			thin, plate = '▁', '▄'
		}
		for i := range inner {
			h.set(row, 3+i, thin, hudOrange)
		}
		for _, p := range hudPlates(rng, inner, 3) {
			for i := p.from; i < p.to; i++ {
				h.set(row, 3+i, plate, hudOrange)
			}
		}
	}
}

// hudPlate is a stretch of an edge drawn thick, as the window's armour
// plates: cells [from, to) along it, hazard-striped or solid.
type hudPlate struct {
	from, to int
	striped  bool
}

// hudPlates places the plates along an edge n cells long, from the frame's
// random source, at most most of them and a cell at least from each end.
func hudPlates(rng *rand.Rand, n, most int) []hudPlate {
	if n < 10 {
		return nil
	}
	var ps []hudPlate
	count := 1 + rng.IntN(most)
	seg := (n - 2) / count
	for i := range count {
		l := max(4, seg/3+rng.IntN(max(1, seg/3)))
		if l >= seg {
			continue
		}
		from := 1 + i*seg + rng.IntN(seg-l)
		ps = append(ps, hudPlate{from: from, to: from + l, striped: rng.IntN(3) == 0})
	}
	return ps
}

// hudCuts are how deep a frame's corners are cut (top left, top right,
// bottom right, bottom left), in cells, as the window cuts them: two big
// and two small crosswise, or all four alike; less on the chat, whose text
// is close to them.
func hudCuts(rng *rand.Rand, chat bool) [4]int {
	big, small := 2+rng.IntN(2), 1
	var k [4]int
	switch rng.IntN(3) {
	case 0:
		k = [4]int{big, small, big, small}
	case 1:
		k = [4]int{small, big, small, big}
	default:
		m := 1 + rng.IntN(2)
		k = [4]int{m, m, m, m}
	}
	if chat {
		for i := range k {
			k[i] = min(k[i], 2)
		}
	}
	return k
}

// hudBox frames the rect of cells top, left, cols x rows (its outermost
// cells are the frame) in colour c: a line of half blocks hugging what is
// inside, the corners cut on a slant into it (a step of quarter blocks a
// row), and plates along the line, their ends slanted too, some
// hazard-striped. Where the corners are cut
// and the plates go follows from seed; mirror flips it all left to right
// (the peer's pane, as the window mirrors its frame).
func hudBox(h *hudCanvas, top, left, cols, rows int, c [3]int, seed uint64, mirror, chat bool) {
	if cols < 6 || rows < 4 {
		return
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x5bd1e995))
	k := hudCuts(rng, chat)
	for i := range k {
		k[i] = max(1, min(k[i], (rows-1)/3, (cols-1)/4))
	}
	right, bottom := left+cols-1, top+rows-1
	set := func(r, col int, ch rune) {
		if mirror {
			col, ch = left+right-col, mirrored(ch)
		}
		h.set(r, col, ch, c)
	}
	// The corners, drawn as the top left one and flipped: a slant from the
	// end of one edge to the end of the other, as thick as they are (two
	// quarters a quarter row, a quarter further on each), and nothing
	// outside it (the video there is cut away). Cut a cell deep, the corner
	// is a quarter joining the two edges.
	corner := func(depth int, across, down bool) {
		put := func(i, j int, ch rune) {
			r, col := top+i, left+j
			if down {
				r, ch = bottom-i, flipped(ch)
			}
			if across {
				col, ch = right-j, mirrored(ch)
			}
			set(r, col, ch)
		}
		if depth == 1 {
			put(0, 0, '▗')
			return
		}
		for j := range depth - 1 {
			put(0, j, ' ')
		}
		put(0, depth-1, '▄')
		for i := 1; i < depth; i++ {
			for j := 0; j < depth-1-i; j++ {
				put(i, j, ' ')
			}
			put(i, depth-1-i, '▟')
			put(i, depth-i, '▘')
		}
	}
	corner(k[0], false, false)
	corner(k[1], true, false)
	corner(k[2], true, true)
	corner(k[3], false, true)

	// edge draws a straight edge of n cells, cell i at pos(i): the thin
	// line, and plates on it with slanted ends.
	edge := func(n int, thin, start, end rune, pos func(i int) (int, int), most int) {
		plates := hudPlates(rng, n, most)
		for i := range n {
			r, col := pos(i)
			ch := thin
			for _, p := range plates {
				switch {
				case i < p.from || i >= p.to:
					continue
				case i == p.from:
					ch = start
				case i == p.to-1:
					ch = end
				case p.striped:
					ch = hazardCell(r, col)
				default:
					ch = '█'
				}
			}
			set(r, col, ch)
		}
	}
	edge(cols-k[0]-k[1], '▄', '▟', '▙', func(i int) (int, int) { return top, left + k[0] + i }, 2)
	edge(cols-k[3]-k[2], '▀', '▜', '▛', func(i int) (int, int) { return bottom, left + k[3] + i }, 2)
	edge(rows-k[0]-k[3], '▐', '▟', '▜', func(i int) (int, int) { return top + k[0] + i, left }, 1)
	edge(rows-k[1]-k[2], '▌', '▙', '▛', func(i int) (int, int) { return top + k[1] + i, right }, 1)
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
	end, mid := "▐", "─"
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
	if end == "▐" {
		end = "▌"
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

// hudVideoMask is the frames' canvas while the panes are drawn: the cells
// it takes (the cut corners) are left out of the video.
var hudVideoMask *hudCanvas
