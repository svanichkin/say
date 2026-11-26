//go:build darwin || windows

package gui

import (
	"image"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
)

// dotImage is one tile of the lattice (two rows), to repeat.
func (g *game) dotImage() *ebiten.Image {
	r := &g.r
	step, row, size := dotGrid(g.cw)
	if r.dots != nil && r.dotsOf == g.cw {
		return r.dots
	}
	if r.dots != nil {
		r.dots.Deallocate()
	}
	img := ebiten.NewImage(step, 2*row)
	pix := make([]byte, 4*step*2*row)
	for y := 0; y < 2*row; y++ {
		for x := 0; x < step; x++ {
			if dotAt(x, y, step, row, size) {
				o := 4 * (y*step + x)
				pix[o], pix[o+1], pix[o+2], pix[o+3] = dotGrey, dotGrey, dotGrey, 255
			}
		}
	}
	img.WritePixels(pix)
	r.dots, r.dotsOf = img, g.cw
	return img
}

// drawDots lays the dots on the text windows' cells of the background's
// colour, inside each one's inner line's box (its cut corners are cut by
// clipPanes), between the cells' backgrounds and their text.
func (g *game) drawDots(dst *ebiten.Image, cols int, cells []Cell, panes []Pane) {
	if cols <= 0 {
		return
	}
	rows := len(cells) / cols
	var vs []ebiten.Vertex
	var is []uint16
	var k float32 // the pane's grey against the tile's
	quad := func(r image.Rectangle) {
		if len(vs)+4 > math.MaxUint16 {
			return
		}
		n := uint16(len(vs))
		for _, p := range [4][2]int{{r.Min.X, r.Min.Y}, {r.Max.X, r.Min.Y}, {r.Max.X, r.Max.Y}, {r.Min.X, r.Max.Y}} {
			x, y := float32(p[0]), float32(p[1])
			vs = append(vs, ebiten.Vertex{DstX: x, DstY: y, SrcX: x, SrcY: y, ColorR: k, ColorG: k, ColorB: k, ColorA: 1})
		}
		is = append(is, n, n+1, n+2, n, n+2, n+3)
	}
	for _, p := range panes {
		if !textPane(p) {
			continue
		}
		k = float32(paneDotGrey(p)) / dotGrey
		box := polyBounds(g.paneFrame(p).Inner)
		c0, c1 := max(0, (box.Min.X-g.ox)/g.cw), min(cols-1, (box.Max.X-g.ox)/g.cw)
		r0, r1 := max(0, (box.Min.Y-g.oy)/g.ch), min(rows-1, (box.Max.Y-g.oy)/g.ch)
		for row := r0; row <= r1; row++ {
			for col := c0; col <= c1; col++ {
				c := cells[row*cols+col]
				if c.Video || c.Bg != defaultBg {
					continue // a block's, a picture's
				}
				cell := image.Rect(g.ox+col*g.cw, g.oy+row*g.ch, g.ox+(col+1)*g.cw, g.oy+(row+1)*g.ch)
				if r := cell.Intersect(box); !r.Empty() {
					quad(r)
				}
			}
		}
	}
	if len(is) == 0 {
		return
	}
	dst.DrawTriangles(vs, is, g.dotImage(), &ebiten.DrawTrianglesOptions{Filter: ebiten.FilterNearest, Address: ebiten.AddressRepeat})
}
