//go:build darwin || windows

package gui

import (
	"image/color"
	"math"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"github.com/svanichkin/say/hud"
)

// The frames' geometry is package hud's, shared with the terminal, which
// shows them as images where it can.

type pt = hud.Pt
type paneFrame = hud.Frame
type frameStyle = hud.Style

func generate(seed uint64, chat bool) frameStyle { return hud.Generate(seed, chat) }
func styleOf(seed uint64, chat bool) frameStyle  { return hud.StyleOf(seed, chat) }

// frameOf lays out pane p's frame in device pixels.
func (g *game) frameOf(p Pane, caption string, st frameStyle) paneFrame {
	return hud.FrameOf(g.grid(), hud.Rect{Row: p.Row, Col: p.Col, Cols: p.Cols, Rows: p.Rows}, p.Remote, caption, st)
}

// grid is the window's cell grid.
func (g *game) grid() hud.Grid { return hud.Grid{CW: g.cw, CH: g.ch, OX: g.ox, OY: g.oy} }

// non-zero rule: it may be concave).
func polygon(poly []pt) ([]ebiten.Vertex, []uint16) {
	var path vector.Path
	path.MoveTo(float32(poly[0][0]), float32(poly[0][1]))
	for _, q := range poly[1:] {
		path.LineTo(float32(q[0]), float32(q[1]))
	}
	path.Close()
	return path.AppendVerticesAndIndicesForFilling(nil, nil)
}

// paneSeed and paneColour select a pane's geometry and accent.
func (g *game) paneSeed(p Pane) uint64 {
	self, peer, chat := uint64(1), uint64(2), uint64(3)
	if g.in.Seeds != nil {
		self, peer, chat = g.in.Seeds()
	}
	switch {
	case p.Chat:
		return chat
	case p.Remote:
		return peer
	}
	return self
}

func (g *game) paneColour(p Pane) color.RGBA {
	if p.Chat {
		return g.r.chatAccent
	}
	if p.Remote {
		return g.r.accent[1]
	}
	return g.r.accent[0]
}

type frameLayoutKey struct {
	row, col, cols, rows int
	cw, ch, ox, oy       int
	caption              string
	seed                 uint64
	remote, chat         bool
}

type cachedPaneFrame struct {
	key   frameLayoutKey
	frame paneFrame
}

func (g *game) paneFrame(p Pane) paneFrame {
	key := g.paneFrameKey(p)
	index := 0
	if p.Remote {
		index = 1
	}
	if p.Chat {
		index = 2
	}
	cached := &g.r.frames[index]
	if cached.frame.Inner == nil || cached.key != key {
		cached.key = key
		cached.frame = g.frameOf(p, p.Caption, styleOf(key.seed, p.Chat))
	}
	return cached.frame
}

// drawPanes draws each pane's video, a pixel per subpixel of its cells,
// fitted into the pane and cut to the frame's inner line.
func (g *game) drawPanes(dst *ebiten.Image, panes []Pane) {
	if g.r.paneImgs == nil {
		g.r.paneImgs = map[[2]int]*paneImage{}
	}
	for _, p := range panes {
		if p.Chat {
			continue
		}
		f := g.frameImage(p, g.paneColour(p))
		if f == nil {
			continue
		}
		fr := g.paneFrame(p)
		px, py := float64(g.ox+p.Col*g.cw), float64(g.oy+p.Row*g.ch)
		pw, ph := float64(p.Cols*g.cw), float64(p.Rows*g.ch)
		iw, ih := float64(f.Bounds().Dx()), float64(f.Bounds().Dy())
		k := math.Min(pw/iw, ph/ih)
		fx, fy := px+(pw-iw*k)/2, py+(ph-ih*k)/2
		vs, is := polygon(fr.Inner)
		for i := range vs {
			vs[i].SrcX = float32((float64(vs[i].DstX) - fx) / k)
			vs[i].SrcY = float32((float64(vs[i].DstY) - fy) / k)
			vs[i].ColorR, vs[i].ColorG, vs[i].ColorB, vs[i].ColorA = 1, 1, 1, 1
		}
		dst.DrawTriangles(vs, is, f, &ebiten.DrawTrianglesOptions{Filter: ebiten.FilterNearest, FillRule: ebiten.FillRuleNonZero})
	}
}

// drawFrames draws the frames over the cells and the text. They change only
// with the layout, a caption's length or a colour, so they are drawn once into a
// layer of their own (and one for their glow) and the layers are laid on
// every frame: drawing them shape by shape, antialiased, takes tens of
// milliseconds and made resizing the window stall.
func (g *game) drawFrames(dst *ebiten.Image, panes []Pane) {
	r := &g.r
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	gw, gh := r.glow.Bounds().Dx(), r.glow.Bounds().Dy()
	key := framesKey{w: w, h: h}
	for _, p := range panes {
		// The caption's plate follows only its length: the text, which
		// changes every second, is written over it (drawCaptions).
		layout := g.paneFrameKey(p)
		layout.caption = strconv.Itoa(utf8.RuneCountInString(p.Caption))
		key.panes = append(key.panes, paneKey{layout, g.paneColour(p)})
	}
	if r.frameLayer == nil || !key.equal(r.framesDrawn) {
		r.frameLayer = grown(r.frameLayer, w, h)
		r.frameGlow = grown(r.frameGlow, gw, gh)
		r.frameLayer.Clear()
		r.frameGlow.Clear()
		g.drawBorder(w, h)
		for _, p := range panes {
			g.frameShapes(p)
		}
		r.framesDrawn = key
	}
	dst.DrawImage(r.frameLayer, nil)
	r.glow.DrawImage(r.frameGlow, nil)
}

// frameShapes draws a pane's frame into the frame layers: its shapes go in
// a few batches, one draw per colour.
func (g *game) frameShapes(p Pane) {
	r := &g.r
	fr := g.paneFrame(p)
	c := g.paneColour(p)
	u := float64(g.cw)
	dimmer := hud.Dimmer(c)
	var dim, bright, cuts, label, dimGlow, glow shapeBatch
	for _, ps := range fr.Rails {
		dim.add(ps, 1)
	}
	inner, outer, trace := hud.Widths(u)
	dim.loop(fr.Inner, inner, 1)
	dimGlow.loop(fr.Inner, inner*2, glowScale)
	bright.loop(fr.Outer, outer, 1)
	glow.loop(fr.Outer, outer*2, glowScale)
	for _, path := range fr.Traces {
		for i := 1; i < len(path); i++ {
			bright.line(path[i-1], path[i], trace, 1)
			glow.line(path[i-1], path[i], trace*2, glowScale)
		}
	}
	shapes := append([][]pt{}, fr.Plates...)
	shapes = append(shapes, fr.Teeth...)
	for _, node := range fr.Nodes {
		shapes = append(shapes, hud.Node(node, u))
	}
	for _, ps := range shapes {
		bright.add(ps, 1)
		glow.add(ps, glowScale)
	}
	for _, ps := range fr.Cuts {
		cuts.add(ps, 1)
	}
	if fr.Label != nil { // a dark plate with a bright edge: the caption glows on it
		label.add(fr.Label, 1)
		bright.loop(fr.Label, outer, 1)
		glow.loop(fr.Label, outer*2, glowScale)
	}
	dim.draw(r.frameLayer, dimmer, r.white)
	label.draw(r.frameLayer, hud.LabelFill(c), r.white)
	bright.draw(r.frameLayer, c, r.white)
	cuts.draw(r.frameLayer, defaultBg, r.white)
	dimGlow.draw(r.frameGlow, dimmer, r.white)
	glow.draw(r.frameGlow, c, r.white)
}

// whiter mixes c with white, k of the way.
func whiter(c color.RGBA, k float64) color.RGBA {
	mix := func(v uint8) uint8 { return uint8(float64(v) + (255-float64(v))*k) }
	return color.RGBA{mix(c.R), mix(c.G), mix(c.B), 255}
}

// framesKey is what the frame layers were drawn for.
type framesKey struct {
	w, h  int
	panes []paneKey
}

type paneKey struct {
	layout frameLayoutKey
	colour color.RGBA
}

func (k framesKey) equal(o framesKey) bool {
	return k.w == o.w && k.h == o.h && slices.Equal(k.panes, o.panes)
}

// grown returns img when it holds w x h, or a new, larger one: layers are
// drawn from the corner, so a larger one does, and resizing the window does
// not allocate on every step.
func grown(img *ebiten.Image, w, h int) *ebiten.Image {
	if img != nil && img.Bounds().Dx() >= w && img.Bounds().Dy() >= h {
		return img
	}
	if img != nil {
		img.Deallocate()
	}
	const step = 256
	return ebiten.NewImage((w+step-1)/step*step, (h+step-1)/step*step)
}

// shapeBatch gathers filled shapes of one colour into one draw. Every shape
// is turned the same way round, so where they overlap they add up (the
// non-zero rule) instead of cancelling.
type shapeBatch struct {
	vs []ebiten.Vertex
	is []uint16
}

func (b *shapeBatch) add(poly []pt, scale float64) {
	if len(poly) < 3 {
		return
	}
	area := 0.0
	for i := range poly {
		a, c := poly[i], poly[(i+1)%len(poly)]
		area += a[0]*c[1] - c[0]*a[1]
	}
	var path vector.Path
	at := func(i int) pt {
		if area < 0 {
			return poly[len(poly)-1-i]
		}
		return poly[i]
	}
	q := at(0)
	path.MoveTo(float32(q[0]*scale), float32(q[1]*scale))
	for i := 1; i < len(poly); i++ {
		q = at(i)
		path.LineTo(float32(q[0]*scale), float32(q[1]*scale))
	}
	path.Close()
	b.vs, b.is = path.AppendVerticesAndIndicesForFilling(b.vs, b.is)
}

// line adds a straight stroke, width wide.
func (b *shapeBatch) line(a, c pt, width, scale float64) {
	dx, dy := c[0]-a[0], c[1]-a[1]
	n := math.Hypot(dx, dy)
	if n == 0 {
		return
	}
	nx, ny := -dy/n*width/2, dx/n*width/2
	b.add([]pt{{a[0] + nx, a[1] + ny}, {c[0] + nx, c[1] + ny}, {c[0] - nx, c[1] - ny}, {a[0] - nx, a[1] - ny}}, scale)
}

// loop adds a closed polyline.
func (b *shapeBatch) loop(ps []pt, width, scale float64) {
	for i := range ps {
		b.line(ps[i], ps[(i+1)%len(ps)], width, scale)
	}
}

// tinted adds a convex shape in a colour of its own (c not premultiplied).
// A batch of tinted shapes is drawn with drawTinted: the shapes overlap and
// blend, later over earlier.
func (b *shapeBatch) tinted(poly []pt, scale float64, c color.RGBA) {
	if len(poly) < 3 {
		return
	}
	base := uint16(len(b.vs))
	a := float32(c.A) / 255
	for _, q := range poly {
		b.vs = append(b.vs, ebiten.Vertex{
			DstX: float32(q[0] * scale), DstY: float32(q[1] * scale),
			ColorR: float32(c.R) / 255 * a, ColorG: float32(c.G) / 255 * a, ColorB: float32(c.B) / 255 * a, ColorA: a,
		})
	}
	for i := 1; i+1 < len(poly); i++ { // a fan: the shape is convex
		b.is = append(b.is, base, base+uint16(i), base+uint16(i+1))
	}
}

func (b *shapeBatch) drawTinted(img *ebiten.Image, white *ebiten.Image) {
	if len(b.is) == 0 {
		return
	}
	img.DrawTriangles(b.vs, b.is, white, &ebiten.DrawTrianglesOptions{AntiAlias: true})
}

func (b *shapeBatch) draw(img *ebiten.Image, c color.RGBA, white *ebiten.Image) {
	if len(b.is) == 0 {
		return
	}
	a := float32(c.A) / 255
	for i := range b.vs {
		b.vs[i].SrcX, b.vs[i].SrcY = 0, 0
		b.vs[i].ColorR, b.vs[i].ColorG, b.vs[i].ColorB, b.vs[i].ColorA = float32(c.R)/255*a, float32(c.G)/255*a, float32(c.B)/255*a, a
	}
	img.DrawTriangles(b.vs, b.is, white, &ebiten.DrawTrianglesOptions{AntiAlias: true, FillRule: ebiten.FillRuleNonZero})
}

// paneFrameKey is what a pane's frame geometry follows from.
func (g *game) paneFrameKey(p Pane) frameLayoutKey {
	return frameLayoutKey{p.Row, p.Col, p.Cols, p.Rows, g.cw, g.ch, g.ox, g.oy, p.Caption, g.paneSeed(p), p.Remote, p.Chat}
}

// drawCaptions writes the captions on their plates: with glow, in their
// frames' colours into the glow layer only (before it is laid on); then,
// crisp and nearly white, over it.
func (g *game) drawCaptions(dst *ebiten.Image, panes []Pane, glow bool) {
	for _, p := range panes {
		if fr := g.paneFrame(p); fr.Label != nil {
			c := g.paneColour(p)
			if !glow {
				c = whiter(c, 0.75) // a neon sign: white letters, the glow in colour
			}
			g.drawCaption(dst, fr, c, glow)
		}
	}
}

// frameImage returns the pane's frame as an image (a pixel per subpixel),
// rasterizing it when it is a new one; a black and white frame is tinted
// with tint, its owner's colour.
func (g *game) frameImage(p Pane, tint color.RGBA) *ebiten.Image {
	if g.in.Frame == nil || g.in.FrameRGB == nil {
		return nil
	}
	f := g.in.Frame(p.Frame)
	if f == nil || f.Cols <= 0 || f.Rows <= 0 {
		return nil
	}
	r := &g.r
	key := [2]int{p.Row, p.Col}
	pi := r.paneImgs[key]
	w, h := f.Cols*cellW, f.Rows*cellH
	if pi == nil || pi.img.Bounds().Dx() != w || pi.img.Bounds().Dy() != h {
		if pi != nil {
			pi.img.Deallocate()
		}
		pi = &paneImage{img: ebiten.NewImage(w, h), pix: make([]byte, w*h*4)}
		r.paneImgs[key] = pi
	}
	if pi.frame != p.Frame {
		g.rasterize(f, pi.pix, w, tint)
		pi.img.WritePixels(pi.pix)
		pi.frame = p.Frame
	}
	return pi.img
}

// drawCaption writes the caption on its plate, digits bold (with glow: into
// the glow layer instead).
func (g *game) drawCaption(dst *ebiten.Image, fr paneFrame, c color.RGBA, glow bool) {
	m := g.font.Metrics()
	top := fr.LabelY + (float64(g.ch)-(m.HAscent+m.HDescent))/2
	x := fr.LabelX
	for _, r := range fr.Caption {
		s := string(r)
		bold := 0.0
		if r >= '0' && r <= '9' {
			bold = g.font.BoldShift()
		}
		op := &text.DrawOptions{}
		op.GeoM.Translate(x, top)
		op.ColorScale.ScaleWithColor(c)
		if glow {
			g.glowText(s, op, bold, 0.5)
		} else {
			g.drawChar(dst, s, op, bold)
		}
		x += float64(g.cw)
	}
}
