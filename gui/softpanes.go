package gui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/hud"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// softPixels lays the window's picture over the software renderer's cells
// (softRenderer), as the window elsewhere draws it: the video panes as
// pictures at their own resolution, fitted and cut to their frames' inner
// line; over them and the cells the HUD (package hud: the frames, the
// window's border, the brackets, with their glow); and the captions on
// their plates. The HUD is drawn again only when the layout changes (a
// caption's plate follows its length, not its text); a new video frame
// recomposes its pane only.
type softPixels struct {
	in      Input
	font    *softFont
	cw, ch  int
	w, h    int
	out     []byte // as softRenderer's pixels: blue, green, red
	key     string
	overlay *image.RGBA // the HUD, premultiplied, transparent elsewhere
	light   []byte      // its glow, added as light (red, green, blue)
	glow    softGlow    // the text's glow
	panes   map[[2]int]*softPane
}

type softPane struct {
	p       Pane
	bounds  image.Rectangle // the pane's cells, in pixels
	mask    *image.Alpha    // inside the frame's inner line, over bounds
	label   image.Rectangle // the caption's plate
	labelAt image.Point     // where the caption's first cell is
	colour  color.RGBA
	frame   *codec.TextFrame // the picture shown
	video   []byte           // it rasterised (RGBA, vw x vh)
	vw, vh  int
	place   image.Rectangle // where the picture goes in the pane
}

func newSoftPixels(in Input, f *softFont, cw, ch int) *softPixels {
	return &softPixels{in: in, font: f, cw: cw, ch: ch, panes: map[[2]int]*softPane{}}
}

// update composes the picture over base (whose changed rectangles are
// baseDirty) and returns the rectangles of out that changed.
func (s *softPixels) update(base *softRenderer, panes []Pane, brackets []Bracket, baseDirty []image.Rectangle) []image.Rectangle {
	full := false
	if s.w != base.w || s.h != base.h {
		s.w, s.h = base.w, base.h
		s.out = make([]byte, s.w*s.h*softBytes)
		full = true
	}
	self, peer := [3]int{0, 255, 128}, [3]int{255, 128, 0}
	if s.in.Accents != nil {
		self, peer = s.in.Accents()
	}
	chat := self
	if s.in.Chat != nil {
		chat = s.in.Chat()
	}
	selfSeed, peerSeed, chatSeed := uint64(1), uint64(2), uint64(3)
	if s.in.Seeds != nil {
		selfSeed, peerSeed, chatSeed = s.in.Seeds()
	}
	gr := hud.Grid{CW: s.cw, CH: s.ch}

	// The HUD, when the layout changed.
	scene := hud.Scene{Border: true, BorderSeed: selfSeed}
	for _, p := range panes {
		c, seed := self, selfSeed
		switch {
		case p.Chat:
			c, seed = chat, chatSeed
		case p.Remote:
			c, seed = peer, peerSeed
		}
		scene.Panes = append(scene.Panes, hud.Pane{
			Rect: hud.Rect{Row: p.Row, Col: p.Col, Cols: p.Cols, Rows: p.Rows}, Remote: p.Remote, Chat: p.Chat,
			Colour: color.RGBA{uint8(c[0]), uint8(c[1]), uint8(c[2]), 255}, Seed: seed,
			Caption: strings.Repeat("0", utf8.RuneCountInString(p.Caption)), // the plate follows the length
		})
	}
	for _, b := range brackets {
		scene.Brackets = append(scene.Brackets, hud.Bracket{Row: b.Row, Col: b.Col, Cols: b.Cols, Spans: b.Spans})
	}
	if key := fmt.Sprintf("%dx%d %+v", s.w, s.h, scene); key != s.key {
		s.key = key
		s.overlay, s.light = hud.RenderLight(scene, gr, s.w, s.h, hud.WindowGlow)
		s.layout(gr, scene, panes)
		full = true
	}

	var dirty []image.Rectangle
	if full || len(baseDirty) > 0 {
		// The cells changed: their glow too, round them.
		s.buildGlow(base)
	}
	if full {
		dirty = []image.Rectangle{image.Rect(0, 0, s.w, s.h)}
	} else {
		reach := glowReach()
		for _, r := range baseDirty {
			dirty = append(dirty, r.Inset(-reach))
		}
	}
	// New pictures and captions.
	for _, p := range panes {
		sp := s.panes[[2]int{p.Row, p.Col}]
		if sp == nil {
			continue
		}
		if sp.p.Caption != p.Caption {
			sp.p.Caption = p.Caption
			dirty = append(dirty, sp.label)
		}
		if p.Chat || s.in.Frame == nil {
			continue
		}
		f := s.in.Frame(p.Frame)
		if f == nil || f == sp.frame || f.Cols <= 0 || f.Rows <= 0 {
			continue
		}
		sp.frame = f
		sp.vw, sp.vh = f.Cols*cellW, f.Rows*cellH
		if len(sp.video) != sp.vw*sp.vh*4 {
			sp.video = make([]byte, sp.vw*sp.vh*4)
		}
		rasterizeFrame(s.in, f, sp.video, sp.vw, sp.colour)
		// Fitted to the pane, centred, as the window elsewhere places it.
		k := math.Min(float64(sp.bounds.Dx())/float64(sp.vw), float64(sp.bounds.Dy())/float64(sp.vh))
		w, h := int(float64(sp.vw)*k), int(float64(sp.vh)*k)
		at := sp.bounds.Min.Add(image.Pt((sp.bounds.Dx()-w)/2, (sp.bounds.Dy()-h)/2))
		sp.place = image.Rectangle{at, at.Add(image.Pt(w, h))}
		dirty = append(dirty, sp.bounds)
	}
	bounds := image.Rect(0, 0, s.w, s.h)
	for i, r := range dirty {
		dirty[i] = r.Intersect(bounds)
		s.compose(base, dirty[i])
	}
	return dirty
}

// layout finds each pane's place, inner line and caption plate.
func (s *softPixels) layout(gr hud.Grid, scene hud.Scene, panes []Pane) {
	old := s.panes
	s.panes = map[[2]int]*softPane{}
	for i, p := range panes {
		hp := scene.Panes[i]
		fr := hud.FrameOf(gr, hp.Rect, p.Remote, hp.Caption, hud.StyleOf(hp.Seed, p.Chat))
		sp := &softPane{p: p, colour: hp.Colour,
			bounds: image.Rect(p.Col*s.cw, p.Row*s.ch, (p.Col+p.Cols)*s.cw, (p.Row+p.Rows)*s.ch)}
		if o := old[[2]int{p.Row, p.Col}]; o != nil && o.bounds == sp.bounds {
			sp.frame, sp.video, sp.vw, sp.vh, sp.place = o.frame, o.video, o.vw, o.vh, o.place
		}
		if !p.Chat {
			sp.mask = polygonMask(fr.Inner, sp.bounds)
		}
		if fr.Label != nil {
			sp.label = polygonBounds(fr.Label).Intersect(image.Rect(0, 0, s.w, s.h))
			sp.labelAt = image.Pt(int(math.Round(fr.LabelX)), int(math.Round(fr.LabelY)))
		}
		s.panes[[2]int{p.Row, p.Col}] = sp
	}
}

// compose redraws out in r: the cells, the pictures in their frames, the
// HUD over them, the captions on their plates.
func (s *softPixels) compose(base *softRenderer, r image.Rectangle) {
	if r.Empty() {
		return
	}
	var in []*softPane
	for _, sp := range s.panes {
		if sp.bounds.Overlaps(r) || sp.label.Overlaps(r) {
			in = append(in, sp)
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := (y*s.w + r.Min.X) * softBytes
		copy(s.out[o:o+r.Dx()*softBytes], base.pix[o:o+r.Dx()*softBytes])
	}
	for _, sp := range in {
		if sp.video == nil || sp.mask == nil {
			continue
		}
		pr := sp.place.Intersect(r)
		for y := pr.Min.Y; y < pr.Max.Y; y++ {
			vy := (y - sp.place.Min.Y) * sp.vh / sp.place.Dy()
			for x := pr.Min.X; x < pr.Max.X; x++ {
				m := int(sp.mask.Pix[(y-sp.bounds.Min.Y)*sp.mask.Stride+x-sp.bounds.Min.X])
				if m == 0 {
					continue
				}
				vx := (x - sp.place.Min.X) * sp.vw / sp.place.Dx()
				v := sp.video[(vy*sp.vw+vx)*4:]
				o := (y*s.w + x) * softBytes
				s.out[o] = blend(s.out[o], v[2], m)
				s.out[o+1] = blend(s.out[o+1], v[1], m)
				s.out[o+2] = blend(s.out[o+2], v[0], m)
			}
		}
	}
	if s.overlay != nil {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			ov := s.overlay.Pix[y*s.overlay.Stride+r.Min.X*4:]
			o := (y*s.w + r.Min.X) * softBytes
			for x := 0; x < r.Dx(); x++ {
				a := int(ov[x*4+3])
				if a != 0 {
					inv := 255 - a
					s.out[o] = uint8(int(ov[x*4+2]) + int(s.out[o])*inv/255)
					s.out[o+1] = uint8(int(ov[x*4+1]) + int(s.out[o+1])*inv/255)
					s.out[o+2] = uint8(int(ov[x*4]) + int(s.out[o+2])*inv/255)
				}
				o += softBytes
			}
		}
	}
	for _, sp := range in {
		if sp.label.Overlaps(r) && sp.p.Caption != "" {
			s.caption(sp, r)
		}
	}
	s.addLight(r)
}

// caption writes a pane's caption on its plate, nearly white (a neon
// sign: white letters, the glow round them in the frame's colour).
func (s *softPixels) caption(sp *softPane, clip image.Rectangle) {
	c := sp.colour
	c = color.RGBA{c.R + uint8(float64(255-c.R)*0.75), c.G + uint8(float64(255-c.G)*0.75), c.B + uint8(float64(255-c.B)*0.75), 255}
	clip = clip.Intersect(sp.label)
	x := sp.labelAt.X
	for _, r := range sp.p.Caption {
		g := s.font.glyph(string(r))
		if g != nil {
			b := g.mask.Bounds()
			for y := 0; y < b.Dy(); y++ {
				py := sp.labelAt.Y + g.at.Y + y
				for gx := 0; gx < b.Dx(); gx++ {
					px := x + g.at.X + gx
					if !image.Pt(px, py).In(clip) {
						continue
					}
					a := int(g.mask.Pix[y*g.mask.Stride+gx])
					if a == 0 {
						continue
					}
					o := (py*s.w + px) * softBytes
					s.out[o] = blend(s.out[o], c.B, a)
					s.out[o+1] = blend(s.out[o+1], c.G, a)
					s.out[o+2] = blend(s.out[o+2], c.R, a)
				}
			}
		}
		x += s.cw
	}
}

// polygonMask rasterises poly (pixel coordinates) into a mask over bounds.
func polygonMask(poly []hud.Pt, bounds image.Rectangle) *image.Alpha {
	mask := image.NewAlpha(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	if len(poly) < 3 || bounds.Empty() {
		return mask
	}
	ras := vector.NewRasterizer(bounds.Dx(), bounds.Dy())
	ras.DrawOp = xdraw.Src
	at := func(p hud.Pt) (float32, float32) {
		return float32(p[0]) - float32(bounds.Min.X), float32(p[1]) - float32(bounds.Min.Y)
	}
	ras.MoveTo(at(poly[0]))
	for _, p := range poly[1:] {
		ras.LineTo(at(p))
	}
	ras.ClosePath()
	ras.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return mask
}

func polygonBounds(poly []hud.Pt) image.Rectangle {
	if len(poly) == 0 {
		return image.Rectangle{}
	}
	x0, y0, x1, y1 := poly[0][0], poly[0][1], poly[0][0], poly[0][1]
	for _, p := range poly[1:] {
		x0, y0 = math.Min(x0, p[0]), math.Min(y0, p[1])
		x1, y1 = math.Max(x1, p[0]), math.Max(y1, p[1])
	}
	return image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1)))
}

// rowsOf returns out's pixels in rect, row after row, as /dev/draw loads
// them.
func (s *softPixels) rowsOf(rect image.Rectangle) []byte {
	out := make([]byte, 0, rect.Dx()*rect.Dy()*softBytes)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		o := (y*s.w + rect.Min.X) * softBytes
		out = append(out, s.out[o:o+rect.Dx()*softBytes]...)
	}
	return out
}
