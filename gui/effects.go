package gui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Video effects over the panes (-video-fx, the window only): drawn over each
// pane's picture after it is rasterized (rasterizeFrame), the peer's and
// ours, cells or pixels alike; the stream and its references are not
// touched. The colour maps (duotones, thermal, posterisation) take each
// pixel's luma alone; the others read its neighbours (edges, glow, shifts)
// or the frame's count (flicker, glitch). Ported from Babe's cmd/babecam.
type postFilter struct {
	label string
	apply func(f *filterState, src, dst *image.RGBA)
}

// filterState is what the filters keep between frames: the frame's
// number, a generator, the glitch under way and scratch planes.
type filterState struct {
	n        int
	rng      *rand.Rand
	glitch   int // frames the glitch still lasts
	gy0, gy1 int // its band
	gkind    int
	gshift   int
	src      *image.RGBA
	tint     [3]float64 // the pane's frame colour as gains (paneTint)
	band     float64    // the hologram's rolling band: where it is (0..1)
	bandAt   time.Time  // when band was moved last
	luma     []float64
	glow     []float64
	tmp      []float64
}

var filters = &filterState{rng: rand.New(rand.NewPCG(1, 2))}

// gradient is a colour map of luma through stops (at 0..1, in order).
type stop struct {
	at      float64
	r, g, b float64
}

func gradientLUT(stops ...stop) (t [256][3]uint8) {
	for v := range 256 {
		x := float64(v) / 255
		k := 1
		for k < len(stops)-1 && x > stops[k].at {
			k++
		}
		a, b := stops[k-1], stops[k]
		u := 0.0
		if b.at > a.at {
			u = min(1, max(0, (x-a.at)/(b.at-a.at)))
		}
		t[v] = [3]uint8{uint8(a.r + (b.r-a.r)*u + 0.5), uint8(a.g + (b.g-a.g)*u + 0.5), uint8(a.b + (b.b-a.b)*u + 0.5)}
	}
	return
}

var (
	lutCyber = gradientLUT(
		stop{0, 12, 0, 28}, stop{0.3, 70, 0, 120}, stop{0.6, 230, 20, 120}, stop{1, 60, 255, 255})
	lutBlade = gradientLUT(
		stop{0, 2, 6, 30}, stop{0.45, 40, 30, 140}, stop{0.8, 255, 50, 170}, stop{1, 255, 210, 235})
	lutThermal = gradientLUT(
		stop{0, 0, 0, 0}, stop{0.2, 20, 20, 170}, stop{0.4, 140, 20, 170}, stop{0.62, 255, 110, 0},
		stop{0.82, 255, 230, 20}, stop{1, 255, 255, 255})
	// Four bands of luma in neon.
	posterColors = [4][3]uint8{{14, 0, 34}, {130, 0, 210}, {255, 20, 150}, {40, 255, 240}}
)

func lumaOf8(p []uint8) int { return (77*int(p[0]) + 150*int(p[1]) + 29*int(p[2])) >> 8 }

func clamp8(v float64) uint8 { return uint8(min(255, max(0, v+0.5))) }

// mapLUT colours every pixel by its luma.
func mapLUT(lut *[256][3]uint8) func(*filterState, *image.RGBA, *image.RGBA) {
	return func(_ *filterState, src, dst *image.RGBA) {
		for i := 0; i+3 < len(src.Pix); i += 4 {
			c := lut[lumaOf8(src.Pix[i:])]
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c[0], c[1], c[2], 255
		}
	}
}

// videoEffects are the effects by their -video-fx names.
var videoEffects = map[string]func(*filterState, *image.RGBA, *image.RGBA){
	"duotone":     mapLUT(&lutCyber),
	"bladerunner": mapLUT(&lutBlade),
	"thermal":     mapLUT(&lutThermal),
	"poster":      posterize,
	"neon":        neonEdges,
	"hologram":    hologram,
	"aberration":  aberration,
	"crt":         crt,
	"glitch":      glitch,
}

// videoEffect is the chosen effect (nil: none).
var videoEffect atomic.Pointer[func(*filterState, *image.RGBA, *image.RGBA)]

// VideoEffectNames are the names -video-fx takes.
func VideoEffectNames() []string {
	var n []string
	for k := range videoEffects {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// SetVideoEffect picks the effect drawn over the video panes ("": none).
func SetVideoEffect(name string) error {
	if name == "" || name == "none" {
		videoEffect.Store(nil)
		videoScan.Store(nil)
		effectExposes.Store(false)
		return nil
	}
	f, ok := videoEffects[name]
	if !ok {
		return fmt.Errorf("unknown -video-fx %q: want one of %s", name, strings.Join(VideoEffectNames(), ", "))
	}
	videoEffect.Store(&f)
	effectExposes.Store(name == "hologram")
	videoScan.Store(nil)
	if sc, ok := effectScans[name]; ok {
		videoScan.Store(&sc)
	}
	return nil
}

// effectScans are the effects' scan lines, drawn over the panes on the
// screen (the first dark rows of every period darkened, the first by alpha,
// the others less and less: a soft line, not a hard one), so that
// they are alike in every pane whatever its picture's size.
var effectScans = map[string]effectScan{
	"hologram": {period: 6, dark: 2, alpha: 0.15},
	"crt":      {period: 4, dark: 2, alpha: 0.4},
}

type effectScan struct {
	period, dark int
	alpha        float32
}

// videoScan is the chosen effect's scan lines (nil: none).
var videoScan atomic.Pointer[effectScan]

// currentScan is the scan lines to draw over the panes (false: none).
func currentScan() (effectScan, bool) {
	if p := videoScan.Load(); p != nil {
		return *p, true
	}
	return effectScan{}, false
}

// effectMu keeps one pane's effect at a time (the panes share the state).
var effectMu sync.Mutex

// applyEffect draws the chosen effect over a pane's w x h picture in pix,
// tint the pane's frame colour.
func applyEffect(pix []byte, w, h int, tint color.RGBA) {
	fp := videoEffect.Load()
	if fp == nil || w <= 0 || h <= 0 || len(pix) < 4*w*h {
		return
	}
	effectMu.Lock()
	defer effectMu.Unlock()
	f := filters
	f.n++
	f.tint = paneTint(tint)
	// The band moves by the clock, both panes' together, a pass taking the
	// longer the later the video (bandPass).
	now := time.Now()
	if !f.bandAt.IsZero() {
		dt := min(now.Sub(f.bandAt), 200*time.Millisecond)
		f.band = math.Mod(f.band+dt.Seconds()/bandPass().Seconds(), 1)
	}
	f.bandAt = now
	r := image.Rect(0, 0, w, h)
	if f.src == nil || f.src.Rect != r {
		f.src = image.NewRGBA(r)
	}
	copy(f.src.Pix, pix[:4*w*h])
	dst := &image.RGBA{Pix: pix[:4*w*h], Stride: 4 * w, Rect: r}
	(*fp)(f, f.src, dst)
}

func posterize(_ *filterState, src, dst *image.RGBA) {
	for i := 0; i+3 < len(src.Pix); i += 4 {
		c := posterColors[min(3, lumaOf8(src.Pix[i:])*4/256)]
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c[0], c[1], c[2], 255
	}
}

// lumaPlane fills f.luma with src's luma (0..1).
func (f *filterState) lumaPlane(src *image.RGBA) []float64 {
	n := len(src.Pix) / 4
	if cap(f.luma) < n {
		f.luma, f.glow, f.tmp = make([]float64, n), make([]float64, n), make([]float64, n)
	}
	f.luma, f.glow, f.tmp = f.luma[:n], f.glow[:n], f.tmp[:n]
	for i := range n {
		f.luma[i] = float64(lumaOf8(src.Pix[4*i:])) / 255
	}
	return f.luma
}

// boxBlur blurs p (w x h) in place, radius r, twice each way (about a
// Gaussian), with f.tmp.
func (f *filterState) boxBlur(p []float64, w, h, r int) {
	for range 2 {
		for y := range h {
			row, out := p[y*w:(y+1)*w], f.tmp[y*w:(y+1)*w]
			s := 0.0
			for x := -r; x <= r; x++ {
				s += row[min(w-1, max(0, x))]
			}
			for x := range w {
				out[x] = s / float64(2*r+1)
				s += row[min(w-1, x+r+1)] - row[max(0, x-r)]
			}
		}
		for x := range w {
			s := 0.0
			for y := -r; y <= r; y++ {
				s += f.tmp[min(h-1, max(0, y))*w+x]
			}
			for y := range h {
				p[y*w+x] = s / float64(2*r+1)
				s += f.tmp[min(h-1, y+r+1)*w+x] - f.tmp[max(0, y-r)*w+x]
			}
		}
	}
}

// neonEdges: the edges (Sobel on luma) glowing, cyan to magenta by their
// direction, the rest black, a bloom around them.
func neonEdges(f *filterState, src, dst *image.RGBA) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	l := f.lumaPlane(src)
	at := func(x, y int) float64 { return l[min(h-1, max(0, y))*w+min(w-1, max(0, x))] }
	mag := f.glow
	hue := make([]float64, w*h)
	for y := range h {
		for x := range w {
			gx := at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1) - at(x-1, y-1) - 2*at(x-1, y) - at(x-1, y+1)
			gy := at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1) - at(x-1, y-1) - 2*at(x, y-1) - at(x+1, y-1)
			m := math.Sqrt(gx*gx + gy*gy)
			m = min(1, max(0, (m-0.12)*2.2)) // the weak ones (noise, skin) out
			mag[y*w+x] = m
			hue[y*w+x] = 0.5 + 0.5*math.Sin(math.Atan2(gy, gx)*2)
		}
	}
	glow := make([]float64, w*h)
	copy(glow, mag)
	f.boxBlur(glow, w, h, max(1, h/72))
	for i := range w * h {
		e, g, u := mag[i], glow[i]*2.2, hue[i]
		// Edge colour between cyan (0, 1, 1) and magenta (1, 0.1, 0.9).
		r, gg, b := u, 1-0.9*u, 1-0.1*u
		v := e + g
		o := 4 * i
		dst.Pix[o] = clamp8(255 * (r*v + e*e*0.5))
		dst.Pix[o+1] = clamp8(255 * (gg*v + e*e*0.5))
		dst.Pix[o+2] = clamp8(255 * (b*v + e*e*0.5))
		dst.Pix[o+3] = 255
	}
}

// hologram: the picture's own colours, less of them and cold, at a strong
// contrast and bright, steady (no flicker), its red and blue split apart
// (chromatic aberration), a faintly brighter band rolling down (the slower
// the later the video: bandPass) and a row now and then jumping aside; the
// scan lines drawn on the screen (effectScan).
func hologram(f *filterState, src, dst *image.RGBA) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	// Sizes in shares of the picture, not its pixels: the panes' pictures
	// differ (the scan lines are drawn on the screen: effectScan).
	bh, jit, ab := max(4, h/14), max(1, w/88), max(1, w/176)
	band := int(f.band * float64(h+2*bh))
	for y := range h {
		shift := 0
		if f.rng.IntN(hologramJitterEvery) == 0 {
			shift = f.rng.IntN(2*jit+1) - jit
		}
		roll := 1.0
		if d := y - band + bh; d >= 0 && d < bh {
			roll = hologramBand
		}
		k := roll // no flicker: it tired the eye
		row, out := src.Pix[y*src.Stride:], dst.Pix[y*dst.Stride:]
		at := func(x int) int { return 4 * min(w-1, max(0, x+shift)) }
		// The exposure was set before (applyLight): a contrast only, as an
		// S-curve (the black and the white stay put, nothing burnt).
		con := func(c uint8) float64 { return hologramCurve[c] * k }
		for x := range w {
			r, g, b := con(row[at(x+ab)]), con(row[at(x)+1]), con(row[at(x-ab)+2])
			// Less colour: the camera's own picture, not a postcard's.
			l := 0.299*r + 0.587*g + 0.114*b
			r, g, b = l+(r-l)*hologramSat, l+(g-l)*hologramSat, l+(b-l)*hologramSat
			// Toned by the frame: its colour at the pixel's luma mixed in.
			t := hologramTone
			out[4*x] = clamp8(r*(1-t) + l*f.tint[0]*t)
			out[4*x+1] = clamp8(g*(1-t) + l*f.tint[1]*t)
			out[4*x+2] = clamp8(b*(1-t) + l*f.tint[2]*t)
			out[4*x+3] = 255
		}
	}
}

// hologramJitterEvery: one row in this many jumps aside in a frame (one in
// 60 shimmered: some five rows a frame).
const hologramJitterEvery = 600

// The hologram's rolling band: how much brighter, and how long a pass
// takes: bandPassBase, and bandPassPerDelay times the incoming video's lag
// (how much longer it travels than at its quickest, SetVideoDelay) more,
// at most bandPassMax: a late picture's band crawls.
const (
	hologramBand     = 1.05
	bandPassBase     = 1500 * time.Millisecond
	bandPassPerDelay = 10
	bandPassMax      = 10 * time.Second
)

// videoDelay is the video's lag now (nil: none known).
var videoDelay atomic.Pointer[func() time.Duration]

// SetVideoDelay gives the effects the incoming video's lag.
func SetVideoDelay(f func() time.Duration) { videoDelay.Store(&f) }

// bandPass is how long the band takes over a pane now.
func bandPass() time.Duration {
	d := time.Duration(0)
	if f := videoDelay.Load(); f != nil {
		d = max(0, (*f)())
	}
	return min(bandPassMax, bandPassBase+bandPassPerDelay*d)
}

// The hologram's contrast: an S-curve, (1-a)x + a(3x²-2x³), steeper in
// the middle by a/2 and flatter at the ends, the black and the white kept
// (a straight stretch burnt the light and drowned the shadows: no tones
// left); its brightness is the exposure's (applyLight), set before it.
const hologramContrast = 0.6

var hologramCurve = func() (t [256]float64) {
	for i := range t {
		x := float64(i) / 255
		t[i] = 255 * ((1-hologramContrast)*x + hologramContrast*x*x*(3-2*x))
	}
	return t
}()

// The hologram's colour: how much of the picture's is kept (1: all), and
// how much of the pane's frame colour (at each pixel's luma) is mixed in.
var (
	hologramSat  = 0.55
	hologramTone = 0.25
)

// paneTint is a frame's colour c scaled to its brightest channel 1: a
// pixel's luma times it is that colour at about the pixel's brightness,
// never past white (scaled to a luma of 1 the light went over 255 and
// came out flat).
func paneTint(c color.RGBA) [3]float64 {
	v := [3]float64{float64(c.R), float64(c.G), float64(c.B)}
	m := max(v[0], v[1], v[2])
	if m <= 0 {
		return [3]float64{1, 1, 1}
	}
	return [3]float64{v[0] / m, v[1] / m, v[2] / m}
}

// aberration: red two pixels right, blue two left.
func aberration(_ *filterState, src, dst *image.RGBA) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	ab := max(1, w/176) // two pixels at the camera's 352
	for y := range h {
		row := src.Pix[y*src.Stride:]
		out := dst.Pix[y*dst.Stride:]
		for x := range w {
			out[4*x] = row[4*min(w-1, x+ab)]
			out[4*x+1] = row[4*x+1]
			out[4*x+2] = row[4*max(0, x-ab)+2]
			out[4*x+3] = 255
		}
	}
}

// crt: scan lines and a vignette.
func crt(_ *filterState, src, dst *image.RGBA) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	cx, cy := float64(w-1)/2, float64(h-1)/2
	for y := range h {
		scan := 1.0 // the scan lines are drawn on the screen (effectScan)
		for x := range w {
			dx, dy := (float64(x)-cx)/cx, (float64(y)-cy)/cy
			k := scan * max(0, 1-0.45*(dx*dx+dy*dy))
			o := 4 * (y*w + x)
			dst.Pix[o] = clamp8(float64(src.Pix[o]) * k)
			dst.Pix[o+1] = clamp8(float64(src.Pix[o+1]) * k * 1.03)
			dst.Pix[o+2] = clamp8(float64(src.Pix[o+2]) * k)
			dst.Pix[o+3] = 255
		}
	}
}

// glitch: now and then, for a few frames, a band slides aside or breaks
// into blocks.
func glitch(f *filterState, src, dst *image.RGBA) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	copy(dst.Pix, src.Pix)
	if f.glitch == 0 && f.rng.IntN(25) == 0 {
		f.glitch = 2 + f.rng.IntN(4)
		f.gy0 = f.rng.IntN(h)
		f.gy1 = min(h, f.gy0+max(2, h/36)+f.rng.IntN(max(1, h/6)))
		f.gkind = f.rng.IntN(2)
		f.gshift = (f.rng.IntN(2)*2 - 1) * (max(1, w/35) + f.rng.IntN(max(1, w/6)))
	}
	if f.glitch == 0 {
		return
	}
	f.glitch--
	switch f.gkind {
	case 0: // the band slides aside, its colours split
		for y := f.gy0; y < f.gy1; y++ {
			row, out := src.Pix[y*src.Stride:], dst.Pix[y*dst.Stride:]
			for x := range w {
				sx := ((x-f.gshift)%w + w) % w
				out[4*x] = row[4*(((sx+3)%w+w)%w)]
				out[4*x+1] = row[4*sx+1]
				out[4*x+2] = row[4*sx+2]
			}
		}
	default: // blocks of the band taken from elsewhere in it
		bs := max(4, w/22)
		for bx := 0; bx < w; bx += bs {
			if f.rng.IntN(3) != 0 {
				continue
			}
			from := (bx + (f.rng.IntN(2)*2-1)*bs*(1+f.rng.IntN(4)) + w) % w
			for y := f.gy0; y < f.gy1; y++ {
				for x := 0; x < bs && bx+x < w && from+x < w; x++ {
					copy(dst.Pix[y*dst.Stride+4*(bx+x):y*dst.Stride+4*(bx+x)+4], src.Pix[y*src.Stride+4*(from+x):])
				}
			}
		}
	}
}

// The exposure (-video-light, and always under the hologram), as a photo
// editor's shadows and highlights with auto levels: the picture's light
// measured on a grid of exposeGrid x exposeGrid cells (their mean luma,
// smoothed, following the picture slowly so that it does not flicker); a
// place lit well (exposeShadow..exposeLight) is left as it is, a darker one
// lifted towards exposeShadow (a face before a bright window), a brighter
// one toned down towards exposeLight (the window), each pixel by its
// place's gain; then the range stretched (the darkest exposeClip of the
// luma after the gains and the brightest before them to exposeLow and
// exposeHigh, at most exposeStretch). Pulling every place to one light flattened the picture
// and the stretch after it burnt it: no tones were left. The stream is not
// touched.
var videoLight atomic.Bool

// SetVideoLight turns the exposure on or off (the hologram has it anyway).
func SetVideoLight(on bool) { videoLight.Store(on) }

// effectExposes: the chosen effect wants the exposure (the hologram).
var effectExposes atomic.Bool

const (
	exposeGrid    = 32
	exposeShadow  = 100.0 // a place darker is lifted to it
	exposeLight   = 170.0 // a place brighter is toned down to it
	exposeLift    = 0.9   // how far (as a power of the ratio)
	exposeDim     = 0.6
	exposeGainMin = 0.75
	exposeGainMax = 3.0
	exposeClip    = 0.01
	exposeLow     = 10.0
	exposeHigh    = 245.0
	exposeStretch = 1.6  // the levels' gain at most
	exposeFollow  = 0.25 // of the way to the new light map each frame
)

// exposeMaps is each pane's light map now, by its picture's buffer.
var exposeMaps sync.Map

// applyLight sets a w x h picture's exposure in pix (see videoLight).
func applyLight(pix []byte, w, h int) {
	if !videoLight.Load() && !effectExposes.Load() || w < 4 || h < 4 || len(pix) < 4*w*h {
		return
	}
	const G = exposeGrid
	gw, gh := min(G, w), min(G, h)
	// The light map: each cell's mean luma.
	var cells [G * G]float64
	var cnt [G * G]int
	var srcHist [256]int // the white point is the picture's own (see below)
	samples := 0
	for y := 0; y < h; y += 2 {
		cy := y * gh / h
		row := pix[4*w*y:]
		for x := 0; x < w; x += 2 {
			c := cy*G + x*gw/w
			l := (77*int(row[4*x]) + 150*int(row[4*x+1]) + 29*int(row[4*x+2])) >> 8
			cells[c] += float64(l)
			cnt[c]++
			srcHist[l]++
			samples++
		}
	}
	for i := range cells {
		if cnt[i] > 0 {
			cells[i] /= float64(cnt[i])
		}
	}
	gains := exposeGains(&pix[0], cells, gw, gh)
	// The gain at each pixel: the map between cell centres (bilinear), its
	// two rows of cells across the picture (a, b) taken once a row of cells.
	a, b := make([]float64, w), make([]float64, w)
	prev := -1
	var hist [256]int
	for y := range h {
		fy := (float64(y)+0.5)*float64(gh)/float64(h) - 0.5
		y0 := min(gh-1, max(0, int(math.Floor(fy))))
		y1, ty := min(gh-1, y0+1), min(1, max(0, fy-float64(y0)))
		if y0 != prev {
			prev = y0
			for x := range w {
				fx := (float64(x)+0.5)*float64(gw)/float64(w) - 0.5
				x0 := min(gw-1, max(0, int(math.Floor(fx))))
				x1, tx := min(gw-1, x0+1), min(1, max(0, fx-float64(x0)))
				a[x] = gains[y0*G+x0]*(1-tx) + gains[y0*G+x1]*tx
				b[x] = gains[y1*G+x0]*(1-tx) + gains[y1*G+x1]*tx
			}
		}
		row := pix[4*w*y:]
		for x := range w {
			g := a[x]*(1-ty) + b[x]*ty
			o := 4 * x
			row[o], row[o+1], row[o+2] = clamp8(float64(row[o])*g), clamp8(float64(row[o+1])*g), clamp8(float64(row[o+2])*g)
			hist[(77*int(row[o])+150*int(row[o+1])+29*int(row[o+2]))>>8]++
		}
	}
	// The range stretched again (auto levels).
	n := w * h
	lo, k, ok := exposeLevels(&hist, n, &srcHist, samples)
	if !ok {
		return
	}
	var lut [256]uint8
	for v := range lut {
		lut[v] = clamp8(exposeLow + (float64(v)-lo)*k)
	}
	for i := 0; i+3 < 4*n; i += 4 {
		pix[i], pix[i+1], pix[i+2] = lut[pix[i]], lut[pix[i+1]], lut[pix[i+2]]
	}
}

// scanAlpha is how dark row y of a period of sc's scan lines is (0: not).
func scanAlpha(sc effectScan, y int) float32 {
	if y >= sc.dark {
		return 0
	}
	return sc.alpha * (1 - 0.5*float32(y)/float32(max(1, sc.dark-1)))
}

// exposeGains is each cell's gain from a light map (each cell's mean luma,
// gw x gh of the grid): the map smoothed, a 3x3 box twice, following key's
// map before slowly (no flicker), then lifted where dark, toned down where
// bright.
func exposeGains(key any, cells [exposeGrid * exposeGrid]float64, gw, gh int) (gains [exposeGrid * exposeGrid]float64) {
	const G = exposeGrid
	for range 2 {
		var t [G * G]float64
		for cy := range gh {
			for cx := range gw {
				s, n := 0.0, 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if y, x := cy+dy, cx+dx; y >= 0 && y < gh && x >= 0 && x < gw {
							s += cells[y*G+x]
							n++
						}
					}
				}
				t[cy*G+cx] = s / float64(n)
			}
		}
		cells = t
	}
	// Following the picture slowly.
	if v, ok := exposeMaps.Load(key); ok {
		prev := v.(*[G * G]float64)
		for i := range cells {
			cells[i] = prev[i] + (cells[i]-prev[i])*exposeFollow
		}
	}
	m := cells
	exposeMaps.Store(key, &m)
	for i, l := range cells {
		g := 1.0
		switch {
		case l < exposeShadow:
			g = math.Pow(exposeShadow/max(4, l), exposeLift)
		case l > exposeLight:
			g = math.Pow(exposeLight/l, exposeDim)
		}
		gains[i] = min(exposeGainMax, max(exposeGainMin, g))
	}
	return gains
}

// exposeLevels is the auto levels' black point and gain: the darkest
// exposeClip of hist (n pixels, the luma after the gains) to exposeLow, the
// brightest exposeClip of srcHist (samples, before them) to exposeHigh;
// false when the range is too narrow to stretch.
func exposeLevels(hist *[256]int, n int, srcHist *[256]int, samples int) (lo, k float64, ok bool) {
	l, hi, acc := 0, 255, 0
	for v := range 256 {
		if acc += hist[v]; float64(acc) >= exposeClip*float64(n) {
			l = v
			break
		}
	}
	// The white point from the picture before the gains: from after them
	// the stretch undid the light places' toning down and lifted the middle
	// with it (a picture lit well came out pale).
	acc = 0
	for v := 255; v >= 0; v-- {
		if acc += srcHist[v]; float64(acc) >= exposeClip*float64(samples) {
			hi = v
			break
		}
	}
	if hi-l < 16 {
		return 0, 0, false
	}
	return float64(l), min(exposeStretch, (exposeHigh-exposeLow)/float64(hi-l)), true
}
