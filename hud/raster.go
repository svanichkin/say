package hud

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// The HUD rasterized on the CPU, for a terminal that shows images: the same
// shapes, in the same order and colours, as the window draws with the GPU
// (package gui), with the glow blurred and added the same way. What is not
// HUD is left transparent: the terminal's own text and the video show
// through.

// Pane is a framed pane of a scene: a video pane or the chat.
type Pane struct {
	Rect
	Remote, Chat bool
	Colour       color.RGBA
	Seed         uint64 // the frame's style (see StyleOf)
}

// Scene is what the HUD draws on a screen.
type Scene struct {
	Panes    []Pane
	Brackets []Bracket
	// Border draws the window's own border, its plates placed by BorderSeed.
	Border     bool
	BorderSeed uint64
}

// Inner is the outline of a pane's video (its frame's inner line).
func (p Pane) Inner(gr Grid) []Pt {
	return FrameOf(gr, p.Rect, p.Remote, "", StyleOf(p.Seed, p.Chat)).Inner
}

const (
	// glowScale is the glow layer's size against the image: it is only
	// seen blurred.
	glowScale = 0.5
	// glowSigma is the blur's, in glow layer pixels, as the window's.
	// glowStrength is how bright the halo is: a quarter of the window's
	// 1.6. The window adds it to what is under it as light; laid over the
	// terminal it is a layer of its own, which covers the video instead of
	// lighting it, and at the window's strength it washed the panes' edges
	// out.
	glowSigma    = 3.0
	glowStrength = 0.4
)

// Render draws scene on a w x h image (premultiplied RGBA, transparent but
// for the HUD), the cells laid out by gr.
func Render(sc Scene, gr Grid, w, h int) *image.RGBA {
	r := &raster{
		img:  image.NewRGBA(image.Rect(0, 0, w, h)),
		glow: image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*glowScale)), max(1, int(float64(h)*glowScale)))),
	}
	if sc.Border {
		r.border(gr, w, h, sc.BorderSeed)
	}
	for _, p := range sc.Panes {
		r.frame(gr, p)
	}
	for _, b := range sc.Brackets {
		shapes := BracketShapes(gr, b)
		r.fill(r.img, shapes, 1, solid(HazardOrange))
		r.fill(r.glow, shapes, glowScale, solid(HazardOrange))
	}
	r.addGlow()
	return r.img
}

type raster struct {
	img, glow *image.RGBA
	z         vector.Rasterizer
	mask      *image.Alpha
}

// paint is what a shape is filled with: a colour, or a pattern.
type paint interface {
	at(x, y int) color.RGBA
}

type solid color.RGBA

func (c solid) at(int, int) color.RGBA { return color.RGBA(c) }

// fill draws polys (scaled) onto dst in p, one coverage for all: where they
// overlap they are drawn once. Each is turned the same way round first, so
// that overlaps add up instead of cancelling. Only their bounding box is
// rasterized.
func (r *raster) fill(dst *image.RGBA, polys [][]Pt, scale float64, p paint) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, poly := range polys {
		for _, q := range poly {
			minX, maxX = math.Min(minX, q[0]*scale), math.Max(maxX, q[0]*scale)
			minY, maxY = math.Min(minY, q[1]*scale), math.Max(maxY, q[1]*scale)
		}
	}
	box := image.Rect(int(math.Floor(minX))-1, int(math.Floor(minY))-1, int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1).Intersect(dst.Bounds())
	if box.Empty() {
		return
	}
	bw, bh := box.Dx(), box.Dy()
	if r.mask == nil || cap(r.mask.Pix) < bw*bh {
		r.mask = image.NewAlpha(image.Rect(0, 0, bw, bh))
	}
	r.mask.Pix = r.mask.Pix[:bw*bh]
	r.mask.Stride, r.mask.Rect = bw, image.Rect(0, 0, bw, bh)
	clear(r.mask.Pix)
	r.z.Reset(bw, bh)
	r.z.DrawOp = draw.Src
	ox, oy := float64(box.Min.X), float64(box.Min.Y)
	for _, poly := range polys {
		if len(poly) < 3 {
			continue
		}
		area := 0.0
		for i := range poly {
			a, c := poly[i], poly[(i+1)%len(poly)]
			area += a[0]*c[1] - c[0]*a[1]
		}
		at := func(i int) Pt {
			if area < 0 {
				return poly[len(poly)-1-i]
			}
			return poly[i]
		}
		q := at(0)
		r.z.MoveTo(float32(q[0]*scale-ox), float32(q[1]*scale-oy))
		for i := 1; i < len(poly); i++ {
			q = at(i)
			r.z.LineTo(float32(q[0]*scale-ox), float32(q[1]*scale-oy))
		}
		r.z.ClosePath()
	}
	r.z.Draw(r.mask, r.mask.Bounds(), image.Opaque, image.Point{})
	// Over: dst = paint*coverage + dst*(1-paint alpha*coverage).
	for y := 0; y < bh; y++ {
		row := r.mask.Pix[y*bw : (y+1)*bw]
		do := dst.PixOffset(box.Min.X, box.Min.Y+y)
		for x, cov := range row {
			if cov == 0 {
				continue
			}
			c := p.at(box.Min.X+x, box.Min.Y+y)
			k := uint32(cov)
			sr, sg, sb, sa := uint32(c.R)*k/255, uint32(c.G)*k/255, uint32(c.B)*k/255, uint32(c.A)*k/255
			d := dst.Pix[do+4*x : do+4*x+4]
			inv := 255 - sa
			d[0] = byte(sr + uint32(d[0])*inv/255)
			d[1] = byte(sg + uint32(d[1])*inv/255)
			d[2] = byte(sb + uint32(d[2])*inv/255)
			d[3] = byte(sa + uint32(d[3])*inv/255)
		}
	}
}

// frame draws a pane's frame as the window's frameShapes does.
func (r *raster) frame(gr Grid, p Pane) {
	fr := FrameOf(gr, p.Rect, p.Remote, "", StyleOf(p.Seed, p.Chat))
	c := p.Colour
	u := float64(gr.CW)
	inner, outer, trace := Widths(u)
	var dim, bright, cuts, label, dimGlow, glow [][]Pt
	dim = append(dim, fr.Rails...)
	dim = append(dim, Loop(fr.Inner, inner)...)
	dimGlow = append(dimGlow, Loop(fr.Inner, inner*2)...)
	bright = append(bright, Loop(fr.Outer, outer)...)
	glow = append(glow, Loop(fr.Outer, outer*2)...)
	for _, path := range fr.Traces {
		for i := 1; i < len(path); i++ {
			if q := Line(path[i-1], path[i], trace); q != nil {
				bright = append(bright, q)
			}
			if q := Line(path[i-1], path[i], trace*2); q != nil {
				glow = append(glow, q)
			}
		}
	}
	shapes := append([][]Pt{}, fr.Plates...)
	shapes = append(shapes, fr.Teeth...)
	for _, node := range fr.Nodes {
		shapes = append(shapes, Node(node, u))
	}
	bright = append(bright, shapes...)
	glow = append(glow, shapes...)
	cuts = append(cuts, fr.Cuts...)
	if fr.Label != nil {
		label = append(label, fr.Label)
		bright = append(bright, Loop(fr.Label, outer)...)
		glow = append(glow, Loop(fr.Label, outer*2)...)
	}
	dimmer := Dimmer(c)
	r.fill(r.img, dim, 1, solid(dimmer))
	r.fill(r.img, label, 1, solid(LabelFill(c)))
	r.fill(r.img, bright, 1, solid(c))
	r.fill(r.img, cuts, 1, solid(Background))
	r.fill(r.glow, dimGlow, glowScale, solid(dimmer))
	r.fill(r.glow, glow, glowScale, solid(c))
}

// border draws the window's own border as the window's drawBorder does.
func (r *raster) border(gr Grid, w, h int, seed uint64) {
	b, ok := BorderOf(w, h, gr.CW, seed)
	if !ok {
		return
	}
	for i, bar := range b.Bars {
		r.fill(r.img, [][]Pt{bar}, 1, hazard{stripe: b.Stripe, x0: b.BarX0[i]})
		r.fill(r.glow, [][]Pt{bar}, glowScale, solid(BarGlow))
	}
	var lines, glow [][]Pt
	for _, l := range b.Lines {
		lines = append(lines, Line(l[0], l[1], b.LineW))
		glow = append(glow, Line(l[0], l[1], b.LineW*2))
	}
	lines = append(lines, b.Plates...)
	glow = append(glow, b.Plates...)
	glow = append(glow, b.Striped...)
	r.fill(r.img, lines, 1, solid(HazardOrange))
	r.fill(r.img, b.Striped, 1, hazard{stripe: b.Stripe, x0: -1, modY: b.StripedModY})
	r.fill(r.glow, glow, glowScale, solid(HazardOrange))
}

// hazard is the hazard stripes as an image: a pixel at x, y takes the
// texture's at x-x0+1 and y+1 (or mod(y, modY)+1), as the window maps it.
type hazard struct {
	stripe int
	x0     float64
	modY   float64
}

func (h hazard) at(x, y int) color.RGBA {
	tx := int(math.Floor(float64(x) - h.x0 + 1))
	ty := y + 1
	if h.modY > 0 {
		ty = int(math.Mod(float64(y), h.modY)) + 1
	}
	if HazardIsOrange(tx, ty, h.stripe) {
		return HazardOrange
	}
	return HazardDark
}

// addGlow blurs the glow layer (three box blurs across and down, close to
// a Gaussian of glowSigma) and adds it, scaled up, onto the image: over the
// transparent parts as light of its own.
func (r *raster) addGlow() {
	gw, gh := r.glow.Bounds().Dx(), r.glow.Bounds().Dy()
	n := gw * gh
	ch := [4][]float32{}
	for c := range ch {
		ch[c] = make([]float32, n)
		for i := 0; i < n; i++ {
			ch[c][i] = float32(r.glow.Pix[i*4+c]) / 255
		}
	}
	// Box widths whose three passes make a Gaussian of glowSigma.
	radius := int((math.Sqrt(12*glowSigma*glowSigma/3+1) - 1) / 2)
	tmp := make([]float32, max(gw, gh))
	blur := func(v []float32, start, count, step int) {
		for i := 0; i < count; i++ {
			tmp[i] = v[start+i*step]
		}
		norm := 1 / float32(2*radius+1)
		var acc float32
		for k := -radius; k <= radius; k++ {
			if k >= 0 && k < count {
				acc += tmp[k]
			}
		}
		for i := 0; i < count; i++ {
			v[start+i*step] = acc * norm
			if j := i + radius + 1; j < count {
				acc += tmp[j]
			}
			if j := i - radius; j >= 0 {
				acc -= tmp[j]
			}
		}
	}
	for c := range ch {
		for range 3 {
			for y := 0; y < gh; y++ {
				blur(ch[c], y*gw, gw, 1)
			}
			for x := 0; x < gw; x++ {
				blur(ch[c], x, gh, gw)
			}
		}
	}
	// Add it on (bilinear, as the window scales it up). The image is
	// premultiplied: light added over transparency becomes coverage.
	w, h := r.img.Bounds().Dx(), r.img.Bounds().Dy()
	xs := make([]struct {
		x0, x1 int
		t      float32
	}, w)
	for x := range xs {
		fx := (float64(x)+0.5)*float64(gw)/float64(w) - 0.5
		x0 := int(math.Floor(fx))
		xs[x].t = float32(fx - float64(x0))
		xs[x].x0, xs[x].x1 = min(max(x0, 0), gw-1), min(max(x0+1, 0), gw-1)
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*float64(gh)/float64(h) - 0.5
		y0 := int(math.Floor(fy))
		ty := float32(fy - float64(y0))
		r0, r1 := min(max(y0, 0), gh-1)*gw, min(max(y0+1, 0), gh-1)*gw
		for x := 0; x < w; x++ {
			sx := xs[x]
			var add [3]float32
			lit := false
			for c := 0; c < 3; c++ {
				v := (ch[c][r0+sx.x0]*(1-sx.t)+ch[c][r0+sx.x1]*sx.t)*(1-ty) +
					(ch[c][r1+sx.x0]*(1-sx.t)+ch[c][r1+sx.x1]*sx.t)*ty
				add[c] = v * glowStrength
				lit = lit || add[c] >= 1.0/255
			}
			if !lit {
				continue
			}
			o := (y*w + x) * 4
			p := r.img.Pix[o : o+4]
			peak := float32(p[3]) / 255
			for c := 0; c < 3; c++ {
				v := min(1, float32(p[c])/255+add[c])
				peak = max(peak, v)
				p[c] = byte(v*255 + 0.5)
			}
			p[3] = byte(min(1, peak)*255 + 0.5)
		}
	}
}
