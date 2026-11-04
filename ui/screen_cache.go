package ui

import (
	"sync/atomic"
	"time"
)

// screenCache remembers what each video cell on the terminal currently
// shows, so a frame rewrites only the cells that changed. A full frame of a
// 200x60 terminal is ~140 KB of escape codes; at 30 fps the terminal itself
// spent more CPU parsing it than Say spent producing the video.
//
// The cache is used only by the renderer goroutine. It must forget a cell
// whenever something else may have drawn over it: see invalidate and reset.
type screenCache struct {
	cols, rows int
	cells      []screenCell
	repaintAt  time.Time
}

// screenCell is a glyph with its foreground and background colours.
type screenCell struct {
	r      rune
	fg, bg [3]int
	set    bool
}

// screenRepaintEvery bounds how long stray output (stderr logs, a terminal
// glitch) can stay on screen: the panes are redrawn in full this often.
const screenRepaintEvery = 2 * time.Second

var (
	paneCache screenCache
	// screenDirty is set when a rendered frame was dropped before reaching
	// the terminal: the cache no longer matches the screen.
	screenDirty atomic.Bool
)

// prepare sizes the cache for the terminal and forgets everything when the
// size changed, a frame was dropped, or a periodic full repaint is due.
func (c *screenCache) prepare(cols, rows int, now time.Time) {
	if c.cols != cols || c.rows != rows || len(c.cells) != cols*rows {
		c.cols, c.rows = cols, rows
		c.cells = make([]screenCell, cols*rows)
		c.repaintAt = now.Add(screenRepaintEvery)
		return
	}
	if screenDirty.Swap(false) || !now.Before(c.repaintAt) {
		c.reset()
		c.repaintAt = now.Add(screenRepaintEvery)
	}
}

// reset forgets every cell: the next frame is drawn in full.
func (c *screenCache) reset() {
	clear(c.cells)
}

// invalidate forgets a rectangle (1-based screen coordinates) that was drawn
// over by something else, such as a centred label.
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
