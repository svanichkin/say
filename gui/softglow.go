package gui

import (
	"image"
	"image/color"

	"github.com/svanichkin/say/hud"
)

// The software renderer's glow, as the window's (render.go): what is in an
// accent colour, the hazard orange or the chat's bricks glows: its text
// (textGlow) and its shapes, the cursor (fully), blurred at half the size
// (hud.BlurGlow) and added on top as light, hud.WindowGlow strong, with the
// frames' glow (hud.RenderLight).

// softTextGlowK is a glowing character's strength in the glow layer: the
// window draws it five times, a little shifted, at textGlow each.
const softTextGlowK = 0.5

type softGlow struct {
	gw, gh int
	ch     [3][]float32 // blurred, gw x gh
}

// glowing reports whether text in colour c glows, as renderer.glows does.
func (s *softPixels) glowing(c color.RGBA) bool {
	rgb := func(v [3]int) color.RGBA { return color.RGBA{uint8(v[0]), uint8(v[1]), uint8(v[2]), 255} }
	c.A = 255
	if s.in.Accents != nil {
		self, peer := s.in.Accents()
		if c == rgb(self) || c == rgb(peer) {
			return true
		}
	}
	if s.in.Chat != nil {
		chat := s.in.Chat()
		if c == rgb(chat) {
			return true
		}
		if s.in.Brick != nil && c == rgb(s.in.Brick(chat)) {
			return true
		}
	}
	return c == hud.HazardOrange
}

// buildGlow draws the glowing cells of base into the glow layer and blurs it.
func (s *softPixels) buildGlow(base *softRenderer) {
	g := &s.glow
	g.gw, g.gh = (s.w+1)/2, (s.h+1)/2
	for c := range g.ch {
		if len(g.ch[c]) != g.gw*g.gh {
			g.ch[c] = make([]float32, g.gw*g.gh)
		} else {
			clear(g.ch[c])
		}
	}
	put := func(x, y int, a float32, c color.RGBA) {
		if x < 0 || y < 0 || x >= s.w || y >= s.h {
			return
		}
		i := (y/2)*g.gw + x/2
		for k, v := range [3]uint8{c.R, c.G, c.B} {
			if f := a * float32(v) / 255; f > g.ch[k][i] {
				g.ch[k][i] = f // the brightest of the four pixels: thin strokes keep their light
			}
		}
	}
	cols, rows := base.cols, base.rows
	for i, c := range base.prev[:cols*rows] {
		if (!c.Shape && c.Text == "") || !s.glowing(c.Fg) {
			continue
		}
		x0, y0 := (i%cols)*s.cw, (i/cols)*s.ch
		if c.Shape {
			for y := 0; y < s.ch; y++ {
				for x := 0; x < s.cw; x++ {
					if c.Pattern&(1<<uint(31-(y*cellH/s.ch*cellW+x*cellW/s.cw))) != 0 {
						put(x0+x, y0+y, 1, c.Fg)
					}
				}
			}
			continue
		}
		gl := base.font.glyph(c.Text)
		if gl == nil {
			continue
		}
		b := gl.mask.Bounds()
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				if a := gl.mask.Pix[y*gl.mask.Stride+x]; a != 0 {
					put(x0+gl.at.X+x, y0+gl.at.Y+y, softTextGlowK*float32(a)/255, c.Fg)
				}
			}
		}
	}
	for c := range g.ch {
		hud.BlurGlow(g.ch[c], g.gw, g.gh)
	}
}

// glowReach is how far a cell's glow reaches past it, in pixels.
func glowReach() int { return (3*hud.GlowRadius + 2) * 2 }

// addLight adds the frames' and the text's glow to out in r.
func (s *softPixels) addLight(r image.Rectangle) {
	g := &s.glow
	for y := r.Min.Y; y < r.Max.Y; y++ {
		fy := float32(y)/2 - 0.25
		y0 := int(fy)
		ty := fy - float32(y0)
		if fy < 0 {
			y0, ty = 0, 0
		}
		y1 := min(y0+1, g.gh-1)
		o := (y*s.w + r.Min.X) * softBytes
		for x := r.Min.X; x < r.Max.X; x++ {
			var add [3]float32 // red, green, blue
			if s.light != nil {
				lo := (y*s.w + x) * 3
				add = [3]float32{float32(s.light[lo]), float32(s.light[lo+1]), float32(s.light[lo+2])}
			}
			if g.gw > 0 {
				fx := float32(x)/2 - 0.25
				x0 := int(fx)
				tx := fx - float32(x0)
				if fx < 0 {
					x0, tx = 0, 0
				}
				x1 := min(x0+1, g.gw-1)
				for c := 0; c < 3; c++ {
					ch := g.ch[c]
					v := (ch[y0*g.gw+x0]*(1-tx)+ch[y0*g.gw+x1]*tx)*(1-ty) + (ch[y1*g.gw+x0]*(1-tx)+ch[y1*g.gw+x1]*tx)*ty
					add[c] += v * hud.WindowGlow * 255
				}
			}
			if add[0] >= 1 || add[1] >= 1 || add[2] >= 1 {
				s.out[o] = addByte(s.out[o], add[2])
				s.out[o+1] = addByte(s.out[o+1], add[1])
				s.out[o+2] = addByte(s.out[o+2], add[0])
			}
			o += softBytes
		}
	}
}

func addByte(v uint8, add float32) uint8 {
	return uint8(min(255, float32(v)+add+0.5))
}
