package gui

import (
	"image"
	"image/color"
)

// softRenderer draws the screen with the CPU into a pixel buffer, for a
// window system without Ebitengine (Plan 9's /dev/draw): the cells'
// backgrounds, their shapes (the picture's block and line characters) and
// their text. It redraws only the cells that changed since the last frame
// and reports the rectangles to send.
//
// The buffer is 24-bit in Plan 9's r8g8b8 order: per pixel the bytes blue,
// green, red.
type softRenderer struct {
	font       *softFont
	cw, ch     int // a cell in pixels
	cols, rows int
	w, h       int
	pix        []byte
	prev       []Cell
	valid      bool // prev is what the buffer shows
}

const softBytes = 3 // per pixel

func newSoftRenderer(f *softFont, cw, ch int) *softRenderer {
	return &softRenderer{font: f, cw: cw, ch: ch}
}

// resize makes the buffer cols x rows cells; everything is drawn again.
func (r *softRenderer) resize(cols, rows int) {
	if cols == r.cols && rows == r.rows && r.pix != nil {
		return
	}
	r.cols, r.rows = cols, rows
	r.w, r.h = cols*r.cw, rows*r.ch
	r.pix = make([]byte, r.w*r.h*softBytes)
	r.prev = make([]Cell, cols*rows)
	r.valid = false
}

// draw brings the buffer up to the cells (cols x rows) and returns the
// rectangles of the buffer that changed, a span of a row each.
func (r *softRenderer) draw(cols, rows int, cells []Cell) []image.Rectangle {
	r.resize(cols, rows)
	if len(cells) < cols*rows {
		return nil
	}
	changed := make([]bool, cols*rows)
	for i := range changed {
		changed[i] = !r.valid || cells[i] != r.prev[i]
	}
	// A wide character covers its cell and the next: both go together.
	for i := range changed {
		if cells[i].Wide && i+1 < len(changed) && (changed[i] || changed[i+1]) {
			changed[i], changed[i+1] = true, true
		}
		if r.valid && r.prev[i].Wide && i+1 < len(changed) && changed[i] {
			changed[i+1] = true // what the old one covered goes too
		}
	}
	var dirty []image.Rectangle
	for row := 0; row < rows; row++ {
		lo, hi := -1, -1
		for col := 0; col < cols; col++ {
			i := row*cols + col
			if !changed[i] {
				continue
			}
			if lo < 0 {
				lo = col
			}
			hi = col
			r.fill(col, row, cells[i].Bg)
		}
		if lo < 0 {
			continue
		}
		for col := lo; col <= hi; col++ {
			if i := row*cols + col; changed[i] {
				r.cell(col, row, cells[i])
			}
		}
		dirty = append(dirty, image.Rect(lo*r.cw, row*r.ch, (hi+1)*r.cw, (row+1)*r.ch))
	}
	copy(r.prev, cells[:cols*rows])
	r.valid = true
	return dirty
}

// fill paints a cell's background.
func (r *softRenderer) fill(col, row int, c color.RGBA) {
	x0, y0 := col*r.cw, row*r.ch
	for y := y0; y < y0+r.ch; y++ {
		o := (y*r.w + x0) * softBytes
		for x := 0; x < r.cw; x++ {
			r.pix[o], r.pix[o+1], r.pix[o+2] = c.B, c.G, c.R
			o += softBytes
		}
	}
}

// cell draws a cell's shape or character over its background.
func (r *softRenderer) cell(col, row int, c Cell) {
	x0, y0 := col*r.cw, row*r.ch
	if c.Shape {
		// The 4x8 pattern, each of its pixels a block of the cell.
		for y := 0; y < r.ch; y++ {
			py := y * cellH / r.ch
			o := ((y0+y)*r.w + x0) * softBytes
			for x := 0; x < r.cw; x++ {
				if c.Pattern&(1<<uint(31-(py*cellW+x*cellW/r.cw))) != 0 {
					r.pix[o], r.pix[o+1], r.pix[o+2] = c.Fg.B, c.Fg.G, c.Fg.R
				}
				o += softBytes
			}
		}
		return
	}
	if c.Text == "" || r.font == nil {
		return
	}
	g := r.font.glyph(c.Text)
	if g == nil {
		return
	}
	width := r.cw
	if c.Wide {
		width *= 2
	}
	b := g.mask.Bounds()
	for y := 0; y < b.Dy(); y++ {
		py := y0 + g.at.Y + y
		if py < y0 || py >= y0+r.ch || py >= r.h {
			continue
		}
		for x := 0; x < b.Dx(); x++ {
			px := x0 + g.at.X + x
			if px < x0 || px >= x0+width || px >= r.w {
				continue
			}
			a := int(g.mask.Pix[y*g.mask.Stride+x])
			if a == 0 {
				continue
			}
			o := (py*r.w + px) * softBytes
			r.pix[o] = blend(r.pix[o], c.Fg.B, a)
			r.pix[o+1] = blend(r.pix[o+1], c.Fg.G, a)
			r.pix[o+2] = blend(r.pix[o+2], c.Fg.R, a)
		}
	}
}

func blend(dst, src uint8, a int) uint8 {
	return uint8((int(dst)*(255-a) + int(src)*a + 127) / 255)
}

// rows returns the buffer's pixels in rect, row after row, as /dev/draw
// loads them.
func (r *softRenderer) rowsOf(rect image.Rectangle) []byte {
	out := make([]byte, 0, rect.Dx()*rect.Dy()*softBytes)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		o := (y*r.w + rect.Min.X) * softBytes
		out = append(out, r.pix[o:o+rect.Dx()*softBytes]...)
	}
	return out
}
