//go:build darwin || windows

package gui

import (
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"
)

// The window's own border: on the left and on the right a bar of hazard
// stripes (orange and black, slanting), its ends cut off and a step cut
// into its inner edge as the frames have; along the top and the bottom a
// thin orange line between them.

var hazardOrange = color.RGBA{255, 140, 0, 255}

// drawBorder draws the border into the frame layers (w x h, the window).
func (g *game) drawBorder(w, h int) {
	r := &g.r
	u := float64(g.cw)
	bw := 1.4 * u // the bars' width
	// Flush with the window's edges: a pixel past them, so that the
	// antialiased outer edge falls outside and no dark seam is left.
	m := -1.0
	line := 0.2*u + 1 // the top and bottom lines' thickness
	W, H := float64(w), float64(h)
	if W < 8*u || H < 8*u {
		return
	}
	r.hazard = hazardTexture(r.hazard, int(bw)+2, h+2, g.cw*3/5)
	top, bottom := m, H-m
	length := bottom - top
	d := bw * 0.45 // how deep the step is
	s1, s2 := top+length*0.3, top+length*0.7
	left := []pt{
		{m, top + bw}, {m + bw, top},
		{m + bw, s1}, {m + bw - d, s1 + d}, {m + bw - d, s2 - d}, {m + bw, s2},
		{m + bw, bottom}, {m, bottom - bw},
	}
	right := make([]pt, len(left))
	for i, q := range left {
		right[len(left)-1-i] = pt{W - q[0], q[1]}
	}
	for _, bar := range [][]pt{left, right} {
		x0 := bar[0][0]
		if bar[0][0] > W/2 {
			x0 = W - m - bw
		}
		var b shapeBatch
		b.add(bar, 1)
		for i := range b.vs {
			b.vs[i].SrcX, b.vs[i].SrcY = b.vs[i].DstX-float32(x0)+1, b.vs[i].DstY+1
			b.vs[i].ColorR, b.vs[i].ColorG, b.vs[i].ColorB, b.vs[i].ColorA = 1, 1, 1, 1
		}
		r.frameLayer.DrawTriangles(b.vs, b.is, r.hazard, &ebiten.DrawTrianglesOptions{AntiAlias: true, FillRule: ebiten.FillRuleNonZero})
		var glow shapeBatch
		glow.add(bar, glowScale)
		glow.draw(r.frameGlow, color.RGBA{110, 60, 0, 255}, r.white)
	}
	// Along the top and the bottom: a line with plates on it, as the frames
	// have, some striped. Where they go follows from our frame's seed, so
	// the window keeps its look.
	var lines, glow, striped shapeBatch
	x0, x1 := m+bw+u, W-m-bw-u
	depth := u
	rng := rand.New(rand.NewPCG(g.paneSeed(Pane{}), 0xb0d3))
	for k, y := range []float64{m + line/2, H - m - line/2} {
		lines.line(pt{x0, y}, pt{x1, y}, line, 1)
		glow.line(pt{x0, y}, pt{x1, y}, line*2, glowScale)
		dir := 1.0 // the plates hang into the window
		if k == 1 {
			dir = -1
		}
		edge := y + dir*line/2
		for _, pl := range edgePlates(rng, x1-x0) {
			a, b := x0+pl.from, x0+pl.to
			d := depth * pl.depth
			ps := []pt{{a, edge}, {b, edge}, {b - d, edge + dir*d}, {a + d, edge + dir*d}}
			if pl.striped {
				striped.add(ps, 1)
			} else {
				lines.add(ps, 1)
			}
			glow.add(ps, glowScale)
		}
	}
	lines.draw(r.frameLayer, hazardOrange, r.white)
	r.hazardH = hazardTexture(r.hazardH, w+2, int(depth)+int(line)+4, g.cw*3/5)
	for i := range striped.vs { // the stripes from a texture as wide as the window
		v := &striped.vs[i]
		v.SrcX = v.DstX + 1
		v.SrcY = float32(math.Mod(float64(v.DstY), depth+line) + 1)
		v.ColorR, v.ColorG, v.ColorB, v.ColorA = 1, 1, 1, 1
	}
	if len(striped.is) > 0 {
		r.frameLayer.DrawTriangles(striped.vs, striped.is, r.hazardH, &ebiten.DrawTrianglesOptions{AntiAlias: true, FillRule: ebiten.FillRuleNonZero})
	}
	glow.draw(r.frameGlow, hazardOrange, r.white)
}

// edgePlate is a plate on the top or bottom line: from..to along it, its
// depth a fraction of the most, plain or striped.
type edgePlate struct {
	from, to, depth float64
	striped         bool
}

// edgePlates lays three to five plates along a line length long, apart
// from each other, one of them at least striped.
func edgePlates(rng *rand.Rand, length float64) []edgePlate {
	n := 3 + rng.IntN(3)
	slot := length / float64(n)
	var out []edgePlate
	for i := range n {
		w := slot * (0.3 + rng.Float64()*0.4)
		from := float64(i)*slot + (slot-w)*rng.Float64()
		out = append(out, edgePlate{from: from, to: from + w, depth: 0.6 + rng.Float64()*0.4, striped: rng.IntN(3) == 0})
	}
	out[rng.IntN(n)].striped = true
	return out
}

// hazardTexture returns img when it is w x h, or draws a new one: orange
// and black stripes slanting at 45 degrees, each stripe wide.
func hazardTexture(img *ebiten.Image, w, h, stripe int) *ebiten.Image {
	if img != nil && img.Bounds().Dx() == w && img.Bounds().Dy() == h {
		return img
	}
	if img != nil {
		img.Deallocate()
	}
	stripe = max(2, stripe)
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 4
			if (x+y)/stripe%2 == 0 {
				pix[o], pix[o+1], pix[o+2] = hazardOrange.R, hazardOrange.G, hazardOrange.B
			} else {
				pix[o], pix[o+1], pix[o+2] = 10, 10, 10
			}
			pix[o+3] = 255
		}
	}
	img = ebiten.NewImage(w, h)
	img.WritePixels(pix)
	return img
}

// drawBrackets draws the brackets (Bracket): a block at each end of the row
// and lines between the pieces of text, in the side bars' orange, the lines
// as thin as the frames' outlines, in the middle of the row, glowing. The
// screen's lock is held.
func (g *game) drawBrackets(dst *ebiten.Image) {
	r := &g.r
	u := float64(g.cw)
	line := max(1.5, u/7)
	var lit, glow shapeBatch
	for _, b := range g.screen.brackets {
		if b.Cols < 4 {
			continue
		}
		x0 := float64(g.ox + b.Col*g.cw)
		x1 := x0 + float64(b.Cols*g.cw)
		y := float64(g.oy+b.Row*g.ch) + float64(g.ch)/2
		bw, bh := u*0.5, float64(g.ch)*0.6 // an end block
		gap := u * 0.5                     // between a line and the text
		rect := func(ax, ay, bx, by float64) []pt { return []pt{{ax, ay}, {bx, ay}, {bx, by}, {ax, by}} }
		shapes := [][]pt{
			rect(x0, y-bh/2, x0+bw, y+bh/2),
			rect(x1-bw, y-bh/2, x1, y+bh/2),
		}
		// A line from the left block to the first text, between the
		// texts, and from the last text to the right block; an empty span
		// (no ping yet) is passed over.
		from := x0 + bw
		for _, sp := range b.Spans {
			if sp[0] >= sp[1] || sp[0] < 1 || sp[1] > b.Cols-1 {
				continue
			}
			tFrom, tTo := x0+float64(sp[0])*u, x0+float64(sp[1])*u
			if tFrom-gap > from {
				shapes = append(shapes, rect(from, y-line/2, tFrom-gap, y+line/2))
			}
			from = tTo + gap
		}
		if from < x1-bw {
			shapes = append(shapes, rect(from, y-line/2, x1-bw, y+line/2))
		}
		for _, ps := range shapes {
			lit.tinted(ps, 1, hazardOrange)
			glow.tinted(ps, glowScale, hazardOrange)
		}
	}
	lit.drawTinted(dst, r.white)
	glow.drawTinted(r.glow, r.white)
}
