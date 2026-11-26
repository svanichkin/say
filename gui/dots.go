package gui

import (
	"image"
	"math"

	"github.com/svanichkin/say/hud"
)

// The text windows (the menus, the status, a call ringing, the chat) have a
// faint grey grid of dots behind their text, as an old monitor's: a dot
// every cell's width along a row, the rows half that apart and each set off
// by half a step from the one above (a rhombic lattice), anchored to the
// window. Only on the background: the chosen line's block and the text
// cover them; the frame's inner line cuts them (clipPanes, the soft mask).

// dotGrey is the dots' grey; dotGreyFaint an inactive window's (a menu's
// side column, framed in grey): fainter, as its text.
const (
	dotGrey      = 75
	dotGreyFaint = 45
)

// paneDotGrey is the grey of the dots in p: faint in a grey-framed menu
// column (one not active).
func paneDotGrey(p Pane) uint8 {
	if p.Menu {
		c := p.Colour
		if max(c.R, c.G, c.B)-min(c.R, c.G, c.B) < 16 { // grey: a side column
			return dotGreyFaint
		}
	}
	return dotGrey
}

// dotGrid is the lattice for cells cw wide: the step along a row, the rows'
// spacing, a dot's side.
func dotGrid(cw int) (step, row, size int) {
	step = max(4, cw)
	return step, step / 2, max(1, (cw+4)/8)
}

// dotAt reports whether pixel x, y is a dot's.
func dotAt(x, y, step, row, size int) bool {
	if y%row >= size {
		return false
	}
	return (x+(y/row)%2*(step/2))%step < size
}

// textPane reports whether p is a text window's (dots behind its text).
func textPane(p Pane) bool { return p.Chat || p.Menu }

// polyBounds is the box round a polygon, in whole pixels inside it.
func polyBounds(poly []hud.Pt) image.Rectangle {
	if len(poly) == 0 {
		return image.Rectangle{}
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range poly {
		x0, y0 = math.Min(x0, p[0]), math.Min(y0, p[1])
		x1, y1 = math.Max(x1, p[0]), math.Max(y1, p[1])
	}
	return image.Rect(int(math.Ceil(x0)), int(math.Ceil(y0)), int(math.Floor(x1)), int(math.Floor(y1)))
}
