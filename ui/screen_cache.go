package ui

import (
	"strings"
	"sync/atomic"
	"time"
)

// screenCache remembers what the call screen currently shows, so a frame
// rewrites only what changed. A full frame of a 200x60 terminal is ~140 KB
// of escape codes; at 30 fps the terminal itself spent more CPU parsing it
// than Say spent producing the video.
//
// Video pane cells are remembered one by one; everything else (borders,
// separators, captions, chat and input lines) as the text drawn on each
// screen row. The cache is used only by the renderer goroutine. It must
// forget whatever something else may have drawn over: see invalidate.
type screenCache struct {
	cols, rows int
	cells      []screenCell
	lines      []string // per screen row (1-based): the non-video text drawn there

	// Rolling refresh: rows are forgotten a few at a time, so the whole
	// screen is redrawn once per screenRepaintEvery without a burst.
	lastPrepare  time.Time
	refreshCarry float64
	refreshRow   int // 0-based row to refresh next
}

// screenCell is a glyph with its foreground and background colours.
type screenCell struct {
	r      rune
	fg, bg [3]int
	set    bool
}

// screenRepaintEvery bounds how long stray output (stderr logs, a terminal
// glitch) can stay on screen.
const screenRepaintEvery = 2 * time.Second

var (
	paneCache screenCache
	// screenDirty is set when a rendered frame was dropped before reaching
	// the terminal: the cache no longer matches the screen.
	screenDirty atomic.Bool
)

// prepare sizes the cache for the terminal, forgets everything when the
// size changed or a frame was dropped, and otherwise forgets the rows due
// for the rolling refresh.
func (c *screenCache) prepare(cols, rows int, now time.Time) {
	if c.cols != cols || c.rows != rows || len(c.cells) != cols*rows {
		c.cols, c.rows = cols, rows
		c.cells = make([]screenCell, cols*rows)
		c.lines = make([]string, rows+1)
		c.lastPrepare, c.refreshCarry, c.refreshRow = now, 0, 0
		screenDirty.Store(false)
		return
	}
	if screenDirty.Swap(false) {
		c.reset()
	}
	elapsed := now.Sub(c.lastPrepare)
	c.lastPrepare = now
	if elapsed <= 0 || rows <= 0 {
		return
	}
	c.refreshCarry += float64(elapsed) / float64(screenRepaintEvery) * float64(rows)
	n := min(int(c.refreshCarry), rows)
	c.refreshCarry -= float64(int(c.refreshCarry))
	for ; n > 0; n-- {
		c.forgetRow(c.refreshRow + 1)
		c.refreshRow = (c.refreshRow + 1) % rows
	}
}

// reset forgets everything: the next frame is drawn in full.
func (c *screenCache) reset() {
	clear(c.cells)
	clear(c.lines)
}

// forgetRow forgets one screen row (1-based): its cells and its text.
func (c *screenCache) forgetRow(row int) {
	if row < 1 || row > c.rows {
		return
	}
	clear(c.cells[(row-1)*c.cols : row*c.cols])
	c.lines[row] = ""
}

// invalidate forgets a rectangle of cells (1-based screen coordinates) that
// was drawn over by something else, such as a centred label.
func (c *screenCache) invalidate(top, left, cols, rows int) {
	if c == nil {
		return
	}
	for r := top; r < top+rows; r++ {
		for col := left; col < left+cols; col++ {
			if i, ok := c.index(r, col); ok {
				c.cells[i] = screenCell{}
			}
		}
	}
}

func (c *screenCache) index(row, col int) (int, bool) {
	if c == nil || row < 1 || col < 1 || row > c.rows || col > c.cols {
		return 0, false
	}
	return (row-1)*c.cols + col - 1, true
}

// update records cell at (row, col) and reports whether it must be drawn.
func (c *screenCache) update(row, col int, cell screenCell) bool {
	i, ok := c.index(row, col)
	if !ok {
		return true
	}
	if c.cells[i] == cell {
		return false
	}
	c.cells[i] = cell
	return true
}

// cell returns what the screen shows at (row, col), if known.
func (c *screenCache) cell(row, col int) (screenCell, bool) {
	i, ok := c.index(row, col)
	if !ok || !c.cells[i].set {
		return screenCell{}, false
	}
	return c.cells[i], true
}

// screenLines collects the non-video text of one frame, row by row (each
// piece positions the cursor itself), so that only changed rows are sent.
type screenLines struct {
	rows []strings.Builder
}

func newScreenLines(rows int) *screenLines {
	return &screenLines{rows: make([]strings.Builder, rows+1)}
}

// at returns the builder for a screen row (1-based); out-of-range rows go
// to a scratch builder that is never sent.
func (l *screenLines) at(row int) *strings.Builder {
	if row < 1 || row >= len(l.rows) {
		return &l.rows[0]
	}
	return &l.rows[row]
}

// flush writes the rows whose text differs from what the screen shows.
func (l *screenLines) flush(out *strings.Builder, c *screenCache) {
	for row := 1; row < len(l.rows); row++ {
		text := l.rows[row].String()
		if text == "" {
			continue
		}
		if row < len(c.lines) {
			if c.lines[row] == text {
				continue
			}
			c.lines[row] = text
		}
		out.WriteString("\x1b[0m")
		out.WriteString(text)
	}
}
