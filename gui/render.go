//go:build darwin || windows

package gui

import (
	"image/color"
	"log"
	"math"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/svanichkin/say/codec"
)

// Drawing the window, back to front:
//
//  1. the video panes, cut to HUD frames (paneFrame) with a neon outline in
//     their owner's accent colour and the caption in a tab;
//  2. the cells' backgrounds (transparent over video) and shapes;
//  3. the text, digits in bold;
//  4. glints on each frame's outline, in a colour of their own, born at
//     random, running and fading (drawBricks);
//  5. glow: what glows (outlines, the cursor, accent text, the bricks) is
//     also drawn into a glow layer, which is blurred a little (a Gaussian,
//     glowShader) and added on top: a soft halo close round it.
//
// With panes on the screen the window is drawn 30 times a second (the
// glints move); otherwise only when the screen changed.

type renderer struct {
	frames [3]cachedPaneFrame // geometry changes only with layout, seed or caption
	// menuFrames are the home screen's columns' frames, by their layout.
	menuFrames map[frameLayoutKey]paneFrame
	bgImg      *ebiten.Image // a pixel per cell: the backgrounds
	bgPix      []byte
	shapes     map[uint32]*ebiten.Image
	paneImgs   map[[2]int]*paneImage
	scan       *ebiten.Image // the effect's scan lines (scanImage)
	dots       *ebiten.Image // a tile of the text windows' dots (dotImage)
	dotsOf     int           // the cells' width it is for
	scanOf     effectScan
	scanCW     int // the cells' width scan was made for (the grid follows it)
	white      *ebiten.Image

	static     *ebiten.Image // the cells and the text, redrawn when they change
	staticGlow *ebiten.Image // their glow

	glints      map[int]*glintSet // by paneIndex
	hazard      *ebiten.Image     // the border's stripes: the side bars'
	hazardH     *ebiten.Image     // and the top and bottom plates'
	frameLayer  *ebiten.Image     // the frames, drawn when they change
	frameGlow   *ebiten.Image     // their glow
	framesDrawn framesKey

	glow       *ebiten.Image // what glows, at glowScale
	blurTmp    *ebiten.Image // the glow blurred across
	blurred    *ebiten.Image // and then down
	accent     [2]color.RGBA // ours, the peer's
	chatAccent color.RGBA
	chatBrick  color.RGBA
}

type paneImage struct {
	img    *ebiten.Image
	frame  uint64
	pix    []byte
	pixels bool     // a picture in pixels at its own size (rasterizeSource)
	gpu    *gpuPane // its GPU images (drawGPU; nil: the CPU draws)
}

// free lets the pane's images go.
func (pi *paneImage) free() {
	pi.img.Deallocate()
	if pi.gpu != nil {
		pi.gpu.dispose()
	}
}

// release frees every image the renderer keeps (all but white): the window
// is hidden, and its layers, the panes' pictures and the caches would hold
// GPU memory all the while. Each is made again when next drawn.
func (r *renderer) release() {
	for _, im := range []**ebiten.Image{&r.bgImg, &r.scan, &r.dots, &r.static, &r.staticGlow,
		&r.hazard, &r.hazardH, &r.frameLayer, &r.frameGlow, &r.glow, &r.blurTmp, &r.blurred} {
		if *im != nil {
			(*im).Deallocate()
			*im = nil
		}
	}
	for _, im := range r.shapes {
		im.Deallocate()
	}
	r.shapes = map[uint32]*ebiten.Image{}
	for _, pi := range r.paneImgs {
		pi.free()
	}
	r.paneImgs = nil
	stencilRelease()
	r.framesDrawn = framesKey{}
	r.dotsOf, r.scanOf, r.scanCW = 0, effectScan{}, 0
}

func (g *game) Draw(dst *ebiten.Image) {
	r := &g.r
	// Hidden (closed to the tray, Cmd+H, minimized): nothing is drawn, and
	// what the drawing keeps on the GPU goes until the window is back.
	if g.hidden {
		if !g.released {
			r.release()
			// One draw on the hidden screen: Ebiten carries out what was
			// deallocated with the next commands it sends.
			dst.Fill(defaultBg)
			g.font = nil // the fonts too, and the memory Go keeps free goes back
			dropUnifont()
			debug.FreeOSMemory()
			g.released = true
			log.Printf("[gui] the window hidden: its images freed")
		}
		return
	}
	if g.released {
		g.released, g.dirty, g.drawnW = false, true, 0 // all of it again
	}
	if r.white == nil {
		r.white = ebiten.NewImage(1, 1)
		r.white.Fill(color.White)
	}
	if g.in.Accents != nil {
		self, peer := g.in.Accents()
		r.accent = [2]color.RGBA{rgb(self), rgb(peer)}
	}
	if g.in.Chat != nil {
		r.chatAccent = rgb(g.in.Chat())
		r.chatBrick = g.brickColour(r.chatAccent)
	}
	drawStart := time.Now() // DEBUG-TEMP: slow frames
	defer func() {
		if d := time.Since(drawStart); d > 100*time.Millisecond {
			log.Printf("[debug] window: a frame took %v to draw", d.Round(time.Millisecond))
		}
	}()
	g.screen.View(func(cols, rows int, cells []Cell, panes []Pane, version uint64) {
		b := dst.Bounds()
		same := !g.dirty && version == g.drawnVersion && b.Dx() == g.drawnW && b.Dy() == g.drawnH
		// Nothing new: the window keeps its picture. With panes the bricks
		// move, but animateEvery is plenty for them. Something new waits
		// for animateEvery too (a resize does not): the panes' frames come
		// apart, each pane's some 30 a second, and each redrew the whole
		// window (60 a second, and the window server with it).
		if (same || !g.dirty && b.Dx() == g.drawnW && b.Dy() == g.drawnH) && time.Since(g.drawnAt) < animateEvery {
			return
		}
		if same && len(panes) == 0 {
			return
		}
		g.drawnAt = time.Now()
		changed := !same
		g.dirty = false
		g.drawnVersion, g.drawnW, g.drawnH = version, b.Dx(), b.Dy()
		// Ebiten's images for antialiased draws: ours, twice the largest
		// layer drawn so (grown rounds the window's size up).
		const step = 256
		stencilFor(2*((b.Dx()+step-1)/step*step), 2*((b.Dy()+step-1)/step*step))
		g.prepareGlow(b.Dx(), b.Dy())
		if changed || g.r.static == nil {
			g.drawStatic(b.Dx(), b.Dy(), cols, cells, panes)
		}
		dst.Fill(defaultBg)
		g.drawPanes(dst, panes)
		dst.DrawImage(g.r.static, nil)
		op := &ebiten.DrawImageOptions{}
		g.r.glow.DrawImage(g.r.staticGlow, op)
		g.clipPanes(dst, panes)
		g.drawFrames(dst, panes)
		g.drawBricks(dst, panes)
		g.drawBrackets(dst)
		g.drawCaptions(dst, panes, true)
		g.glowOver(dst)
		g.drawCaptions(dst, panes, false)
	})
}

func rgb(c [3]int) color.RGBA {
	return color.RGBA{byte(c[0]), byte(c[1]), byte(c[2]), 255}
}

// glows reports whether a colour is one of the accents (ours, the peer's,
// the chat's, the chat bricks' that its status text has) or the side bars'
// orange (the ping between the panes): text and shapes in them glow.
func (r *renderer) glows(c color.RGBA) bool {
	return c == r.accent[0] || c == r.accent[1] || c == r.chatAccent || c == r.chatBrick || c == hazardOrange
}

// drawCells draws the backgrounds (transparent over video) as one image of
// a pixel per cell, then the shapes.
func (g *game) drawCells(dst *ebiten.Image, cols int, cells []Cell) {
	r := &g.r
	rows := len(cells) / max(1, cols)
	if r.bgImg == nil || r.bgImg.Bounds().Dx() != cols || r.bgImg.Bounds().Dy() != rows {
		if r.bgImg != nil {
			r.bgImg.Deallocate()
		}
		r.bgImg = ebiten.NewImage(cols, rows)
		r.bgPix = make([]byte, cols*rows*4)
	}
	for i, c := range cells {
		p := r.bgPix[i*4 : i*4+4]
		if c.Video {
			p[0], p[1], p[2], p[3] = 0, 0, 0, 0
			continue
		}
		p[0], p[1], p[2], p[3] = c.Bg.R, c.Bg.G, c.Bg.B, 255
	}
	r.bgImg.WritePixels(r.bgPix)
	op := &ebiten.DrawImageOptions{}
	op.Filter = ebiten.FilterNearest
	op.GeoM.Scale(float64(g.cw), float64(g.ch))
	op.GeoM.Translate(float64(g.ox), float64(g.oy))
	dst.DrawImage(r.bgImg, op)

	for i, c := range cells {
		if !c.Shape || c.Pattern == 0 {
			continue
		}
		op := &ebiten.DrawImageOptions{}
		op.Filter = ebiten.FilterNearest
		op.GeoM.Scale(float64(g.cw)/cellW, float64(g.ch)/cellH)
		op.GeoM.Translate(float64(g.ox+(i%cols)*g.cw), float64(g.oy+(i/cols)*g.ch))
		op.ColorScale.ScaleWithColor(c.Fg)
		dst.DrawImage(r.shape(c.Pattern), op)
		if r.glows(c.Fg) {
			op.GeoM.Scale(glowScale, glowScale)
			r.glow.DrawImage(r.shape(c.Pattern), op) // the cursor
		}
	}
}

// shape returns a 4x8 white mask of a cell pattern.
func (r *renderer) shape(p uint32) *ebiten.Image {
	if img, ok := r.shapes[p]; ok {
		return img
	}
	pix := make([]byte, cellW*cellH*4)
	for i := 0; i < 32; i++ {
		if p&(1<<uint(31-i)) != 0 {
			pix[i*4], pix[i*4+1], pix[i*4+2], pix[i*4+3] = 255, 255, 255, 255
		}
	}
	img := ebiten.NewImage(cellW, cellH)
	img.WritePixels(pix)
	r.shapes[p] = img
	return img
}

// rasterize draws a frame's cells into pix (see rasterizeFrame).
func (g *game) rasterize(f *codec.TextFrame, pix []byte, w int, tint color.RGBA) {
	rasterizeFrame(g.in, f, pix, w, tint)
}

// drawText draws each cell's character in the window's font, centred in its
// row: a character a cell, as in a terminal. Digits are bold; text in an
// accent colour glows.
func (g *game) drawText(dst *ebiten.Image, cols int, cells []Cell) {
	m := g.font.Metrics()
	top := (float64(g.ch) - (m.HAscent + m.HDescent)) / 2
	for i, c := range cells {
		if c.Text == "" || c.Video {
			continue
		}
		op := &text.DrawOptions{}
		op.GeoM.Translate(float64(g.ox+(i%cols)*g.cw), float64(g.oy+(i/cols)*g.ch)+top)
		op.ColorScale.ScaleWithColor(c.Fg)
		bold := 0.0
		if len(c.Text) == 1 && c.Text[0] >= '0' && c.Text[0] <= '9' {
			bold = g.font.BoldShift()
		}
		g.drawChar(dst, c.Text, op, bold)
		if g.r.glows(c.Fg) {
			g.glowText(c.Text, op, bold, textGlow)
		}
	}
}

// drawChar draws s placed by op and, to look bold as bitmap fonts do, again
// bold pixels to the right (0: not bold).
func (g *game) drawChar(dst *ebiten.Image, s string, op *text.DrawOptions, bold float64) {
	text.Draw(dst, s, g.font.Face(), op)
	if bold > 0 {
		o := *op
		o.GeoM.Translate(bold, 0)
		text.Draw(dst, s, g.font.Face(), &o)
	}
}

// textGlow is how strongly text glows: the five copies of glowText add up
// to about this in the middle of a stroke, less round it.
const textGlow = 0.3

// glowText draws text (placed by op, at the window's scale) into the glow
// layer, thickened by drawing it a little shifted every way, each copy at
// strength k: thin letters at half the resolution would leave almost
// nothing to blur.
func (g *game) glowText(s string, op *text.DrawOptions, bold float64, k float32) {
	for _, d := range [][2]float64{{0, 0}, {-0.7, 0}, {0.7, 0}, {0, -0.7}, {0, 0.7}} {
		o := *op
		o.ColorScale.Scale(k, k, k, k)
		o.GeoM.Scale(glowScale, glowScale)
		o.GeoM.Translate(d[0], d[1])
		g.drawChar(g.r.glow, s, &o, bold*glowScale)
	}
}

// brickColour is the bricks' colour on a frame of colour c (Input.Brick):
// one that stands out from it.
func (g *game) brickColour(c color.RGBA) color.RGBA {
	if g.in.Brick == nil {
		return c
	}
	return rgb(g.in.Brick([3]int{int(c.R), int(c.G), int(c.B)}))
}

// animateEvery is the shortest time between frames drawn only for the
// bricks (30 per second).
const animateEvery = 33 * time.Millisecond

// Glints run along the frames' outlines: each is born at a random place
// and time, runs one way or the other at a speed of its own, some
// flickering, and fades from its birth to nothing.
const (
	glintEvery    = 900 * time.Millisecond // on average, between two on a frame
	glintMax      = 4                      // at most at once on a frame
	glintSpeedMin = 6.0                    // in cell widths a second
	glintSpeedMax = 22.0
	glintLifeMin  = 400 * time.Millisecond
	glintLifeMax  = 1600 * time.Millisecond
	glintFadeIn   = 60 * time.Millisecond
)

type glint struct {
	at, dir, speed float64 // where it was born (0..1 round), which way, how fast
	born           time.Time
	life           time.Duration
	flicker        float64 // flickers this many times a second (0: steady)
}

// drawBricks draws the glints of each frame: a little thicker than the
// outline, whiter towards the middle, with a round halo fading outwards.
// All are drawn in two batches (the window and the glow layer), whatever
// their number: every antialiased draw costs.
func (g *game) drawBricks(dst *ebiten.Image, panes []Pane) {
	r := &g.r
	now := time.Now()
	if r.glints == nil {
		r.glints = map[int]*glintSet{}
	}
	u := float64(g.cw)
	var lit, halo shapeBatch
	for _, p := range panes {
		ol := g.paneFrame(p).Outer
		length := outlineLength(ol)
		if length == 0 {
			continue
		}
		set := r.glints[paneIndex(p)]
		if set == nil {
			set = &glintSet{next: now.Add(randDuration(0, glintEvery))}
			r.glints[paneIndex(p)] = set
		}
		set.spawn(now)
		bc := g.brickColour(g.paneColour(p))
		for _, gl := range set.live {
			age := now.Sub(gl.born)
			a := 1 - float64(age)/float64(gl.life) // fading from birth
			if age < glintFadeIn {
				a *= float64(age) / float64(glintFadeIn)
			}
			if gl.flicker > 0 && math.Sin(2*math.Pi*gl.flicker*age.Seconds()) < 0 {
				a *= 0.15
			}
			if a <= 0.01 {
				continue
			}
			at := math.Mod(gl.at+gl.dir*gl.speed*u*age.Seconds()/length, 1)
			if at < 0 {
				at++
			}
			fade := func(c color.RGBA, k float64) color.RGBA {
				return color.RGBA{c.R, c.G, c.B, uint8(255 * k * a)}
			}
			halo.tinted(brickHalo(ol, at, 1.6*u, 0.7*u), glowScale, fade(bc, 0.27))
			halo.tinted(brickHalo(ol, at, 1.1*u, 0.4*u), glowScale, fade(bc, 0.47))
			halo.tinted(brickHalo(ol, at, 0.75*u, 0.2*u), glowScale, fade(whiter(bc, 0.6), 1))
			lit.tinted(brickShape(ol, at, u, 1, 1), 1, fade(bc, 1))
			lit.tinted(brickShape(ol, at, u, 0.75, 0.7), 1, fade(whiter(bc, 0.5), 1))
			lit.tinted(brickShape(ol, at, u, 0.45, 0.45), 1, fade(whiter(bc, 0.85), 1))
		}
	}
	lit.drawTinted(dst, r.white)
	halo.drawTinted(r.glow, r.white)
}

// glintSet is a frame's glints and when the next is born.
type glintSet struct {
	live []glint
	next time.Time
}

// spawn drops the glints that have faded and gives birth to new ones when
// their time comes.
func (s *glintSet) spawn(now time.Time) {
	kept := s.live[:0]
	for _, gl := range s.live {
		if now.Sub(gl.born) < gl.life {
			kept = append(kept, gl)
		}
	}
	s.live = kept
	for !now.Before(s.next) {
		if len(s.live) < glintMax {
			gl := glint{
				at:    rand.Float64(),
				dir:   1,
				speed: glintSpeedMin + rand.Float64()*(glintSpeedMax-glintSpeedMin),
				born:  s.next,
				life:  randDuration(glintLifeMin, glintLifeMax),
			}
			if rand.IntN(2) == 0 {
				gl.dir = -1
			}
			if rand.IntN(3) == 0 {
				gl.flicker = 8 + rand.Float64()*10
			}
			s.live = append(s.live, gl)
		}
		s.next = s.next.Add(randDuration(glintEvery/4, glintEvery*7/4))
		if now.Sub(s.next) > glintEvery { // after a pause (no frames drawn)
			s.next = now
		}
	}
}

func randDuration(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(rand.Int64N(int64(hi-lo)+1))
}

// paneIndex tells the panes apart: ours, the peer's, the chat's, the
// home screen's columns.
func paneIndex(p Pane) int {
	switch {
	case p.Menu: // the home screen's columns, by where they are
		return 3 + p.Col
	case p.Chat:
		return 2
	case p.Remote:
		return 1
	}
	return 0
}

// outlineLength is the length round a closed outline.
func outlineLength(ol []pt) float64 {
	total := 0.0
	for i := range ol {
		total += math.Hypot(ol[(i+1)%len(ol)][0]-ol[i][0], ol[(i+1)%len(ol)][1]-ol[i][1])
	}
	return total
}

// brickShape is a brick on the outline, at of its length round, lying
// along it; u is the cell's width, kl and kt scale its length and its
// thickness about its centre.
func brickShape(ol []pt, at, u, kl, kt float64) []pt {
	x, y, dx, dy, ok := alongOutline(ol, at)
	if !ok {
		return nil
	}
	// A glint: short, a little thicker than the outline (u/7).
	half, thick := u*0.7*kl, max(1.2, u*0.11)*kt // half its length, half its thickness
	return []pt{
		{x - dx*half - dy*thick, y - dy*half + dx*thick},
		{x + dx*half - dy*thick, y + dy*half + dx*thick},
		{x + dx*half + dy*thick, y + dy*half - dx*thick},
		{x - dx*half + dy*thick, y - dy*half - dx*thick},
	}
}

// brickHalo is an ellipse round a brick, rx along the outline and ry
// across it: the glow's source, round so that the blurred halo is too.
func brickHalo(ol []pt, at, rx, ry float64) []pt {
	x, y, dx, dy, ok := alongOutline(ol, at)
	if !ok {
		return nil
	}
	const n = 20
	ps := make([]pt, n)
	for i := range ps {
		a := 2 * math.Pi * float64(i) / n
		c, s := math.Cos(a)*rx, math.Sin(a)*ry
		ps[i] = pt{x + dx*c - dy*s, y + dy*c + dx*s}
	}
	return ps
}

// alongOutline finds the point at of the way round a closed outline and the
// direction of the outline there.
func alongOutline(ol []pt, at float64) (x, y, dx, dy float64, ok bool) {
	d := at * outlineLength(ol)
	for i := range ol {
		a, b := ol[i], ol[(i+1)%len(ol)]
		n := math.Hypot(b[0]-a[0], b[1]-a[1])
		if d > n {
			d -= n
			continue
		}
		if n == 0 {
			return 0, 0, 0, 0, false
		}
		dx, dy = (b[0]-a[0])/n, (b[1]-a[1])/n
		return a[0] + dx*d, a[1] + dy*d, dx, dy, true
	}
	return 0, 0, 0, 0, false
}

// drawStatic redraws the cells and the text into their own layer (and
// their glow into its own): a frame where only the video changed just lays
// it over the video.
func (g *game) drawStatic(w, h, cols int, cells []Cell, panes []Pane) {
	r := &g.r
	gw, gh := r.glow.Bounds().Dx(), r.glow.Bounds().Dy()
	r.static = grown(r.static, w, h)
	r.staticGlow = grown(r.staticGlow, gw, gh)
	r.static.Clear()
	r.staticGlow.Clear()
	glow := r.glow
	r.glow = r.staticGlow // their glow goes to its own layer
	g.drawCells(r.static, cols, cells)
	g.drawDots(r.static, cols, cells, panes) // under the text (dots.go)
	g.drawText(r.static, cols, cells)
	r.glow = glow
}

// glowScale is the size of the glow layer against the window: it is only
// ever seen blurred, so half the resolution (a quarter of the pixels) does.
const glowScale = 0.5

// prepareGlow clears the glow layer, sized to the window.
func (g *game) prepareGlow(w, h int) {
	r := &g.r
	gw, gh := max(1, int(float64(w)*glowScale)), max(1, int(float64(h)*glowScale))
	if r.glow == nil || r.glow.Bounds().Dx() != gw || r.glow.Bounds().Dy() != gh {
		if r.glow != nil {
			r.glow.Deallocate()
			r.blurTmp.Deallocate()
			r.blurred.Deallocate()
		}
		r.glow = newLayer(gw, gh)
		r.blurTmp = newLayer(gw, gh)
		r.blurred = newLayer(gw, gh)
	}
	r.glow.Clear()
}

// glowSource blurs an image along Dir with a Gaussian of glowSigma pixels
// (of the glow layer, half the window's).
const glowSource = `//kage:unit pixels
package main

var Dir vec2

func Fragment(dst vec4, src vec2, color vec4) vec4 {
	const sigma = 3.0
	var sum vec4
	wsum := 0.0
	for i := -9; i <= 9; i++ {
		w := exp(-float(i*i) / (2.0 * sigma * sigma))
		sum += imageSrc0At(src+Dir*float(i)) * w
		wsum += w
	}
	return sum / wsum
}
`

// glowStrength is how bright the halo is added.
const glowStrength = 1.6

var glowShader *ebiten.Shader

// glowOver blurs the glow layer across and down and adds it on top of dst.
func (g *game) glowOver(dst *ebiten.Image) {
	r := &g.r
	if glowShader == nil {
		sh, err := ebiten.NewShader([]byte(glowSource))
		if err != nil {
			panic(err)
		}
		glowShader = sh
	}
	w, h := r.glow.Bounds().Dx(), r.glow.Bounds().Dy()
	pass := func(to, from *ebiten.Image, dx, dy float32) {
		to.Clear()
		op := &ebiten.DrawRectShaderOptions{Uniforms: map[string]any{"Dir": []float32{dx, dy}}}
		op.Images[0] = from
		to.DrawRectShader(w, h, glowShader, op)
	}
	pass(r.blurTmp, r.glow, 1, 0)
	pass(r.blurred, r.blurTmp, 0, 1)
	op := &ebiten.DrawImageOptions{}
	op.Filter = ebiten.FilterLinear
	op.Blend = ebiten.BlendLighter
	op.GeoM.Scale(float64(dst.Bounds().Dx())/float64(w), float64(dst.Bounds().Dy())/float64(h))
	op.ColorScale.Scale(glowStrength, glowStrength, glowStrength, glowStrength)
	dst.DrawImage(r.blurred, op)
}
