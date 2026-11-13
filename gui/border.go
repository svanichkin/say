//go:build darwin || windows

package gui

import (
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/svanichkin/say/hud"
)

// The window's own border: on the left and on the right a bar of hazard
// stripes (orange and black, slanting), its ends cut off and a step cut
// into its inner edge as the frames have; along the top and the bottom a
// thin orange line between them.

var hazardOrange = hud.HazardOrange

// drawBorder draws the border into the frame layers (w x h, the window).
// Where its plates go follows from our frame's seed, so the window keeps
// its look.
func (g *game) drawBorder(w, h int) {
	r := &g.r
	b, ok := hud.BorderOf(w, h, g.cw, g.paneSeed(Pane{}))
	if !ok {
		return
	}
	r.hazard = hazardTexture(r.hazard, int(b.BarW)+2, h+2, b.Stripe)
	for i, bar := range b.Bars {
		var fill shapeBatch
		fill.add(bar, 1)
		for k := range fill.vs {
			fill.vs[k].SrcX, fill.vs[k].SrcY = fill.vs[k].DstX-float32(b.BarX0[i])+1, fill.vs[k].DstY+1
			fill.vs[k].ColorR, fill.vs[k].ColorG, fill.vs[k].ColorB, fill.vs[k].ColorA = 1, 1, 1, 1
		}
		r.frameLayer.DrawTriangles(fill.vs, fill.is, r.hazard, &ebiten.DrawTrianglesOptions{AntiAlias: true, FillRule: ebiten.FillRuleNonZero})
		var glow shapeBatch
		glow.add(bar, glowScale)
		glow.draw(r.frameGlow, hud.BarGlow, r.white)
	}
	var lines, glow, striped shapeBatch
	for _, l := range b.Lines {
		lines.line(l[0], l[1], b.LineW, 1)
		glow.line(l[0], l[1], b.LineW*2, glowScale)
	}
	for _, ps := range b.Plates {
		lines.add(ps, 1)
		glow.add(ps, glowScale)
	}
	for _, ps := range b.Striped {
		striped.add(ps, 1)
		glow.add(ps, glowScale)
	}
	lines.draw(r.frameLayer, hazardOrange, r.white)
	r.hazardH = hazardTexture(r.hazardH, w+2, int(b.StripedModY)+4, b.Stripe)
	for i := range striped.vs { // the stripes from a texture as wide as the window
		v := &striped.vs[i]
		v.SrcX = v.DstX + 1
		v.SrcY = float32(math.Mod(float64(v.DstY), b.StripedModY) + 1)
		v.ColorR, v.ColorG, v.ColorB, v.ColorA = 1, 1, 1, 1
	}
	if len(striped.is) > 0 {
		r.frameLayer.DrawTriangles(striped.vs, striped.is, r.hazardH, &ebiten.DrawTrianglesOptions{AntiAlias: true, FillRule: ebiten.FillRuleNonZero})
	}
	glow.draw(r.frameGlow, hazardOrange, r.white)
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
			c := hud.HazardDark
			if hud.HazardIsOrange(x, y, stripe) {
				c = hazardOrange
			}
			pix[o], pix[o+1], pix[o+2] = c.R, c.G, c.B
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
	var lit, glow shapeBatch
	for _, b := range g.screen.brackets {
		for _, ps := range hud.BracketShapes(g.grid(), hud.Bracket{Row: b.Row, Col: b.Col, Cols: b.Cols, Spans: b.Spans}) {
			lit.tinted(ps, 1, hazardOrange)
			glow.tinted(ps, glowScale, hazardOrange)
		}
	}
	lit.drawTinted(dst, r.white)
	glow.drawTinted(r.glow, r.white)
}
