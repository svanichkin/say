//go:build darwin || windows

package gui

import (
	"image"
	"image/color"
	"log"
	"math"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/ui"
)

// The video in pixels drawn by the GPU (Kage: Metal on macOS, DirectX on
// Windows): the picture's Y, Cb and Cr go up as they are, and two shaders
// do what rasterizeSource, applyLight and the hologram do on the CPU:
// videoShaderSrc the sharpness, the display gamma, the colour (or the
// monochrome tint) and the colour filter; lightShaderSrc the exposure (the
// light map's gains, the auto levels) and the hologram. The CPU keeps what
// needs the whole picture: the light map and the levels, from every other
// pixel of every other row, and the hologram's band and jumping rows. The
// other effects (duotone, neon, ...) and Plan 9 (no GPU: /dev/draw) draw on
// the CPU as before. On a call at 278x288 the window's CPU drawing was
// some 22% of Say's CPU (sharpening, colour, light, hologram).

const videoShaderSrc = `//kage:unit pixels

package main

var Sharp float     // the sharpness in percent (0: off)
var Gamma float     // the display gamma
var Mono float      // 1: the luma in Tint
var Tint vec3       // 0..255
var Filter float    // 1: the colour filter
var FContrast float
var FStrength float
var FTint vec3 // 0..255

func at(x, y float) vec4 {
	s := imageSrc0Size()
	return imageSrc0At(imageSrc0Origin() + vec2(clamp(x, 0, s.x-1)+0.5, clamp(y, 0, s.y-1)+0.5))
}

func lum(x, y float) float {
	return floor(at(x, y).r*255 + 0.5)
}

func clampColor(v float) float {
	return floor(clamp(v, 0, 255) + 0.5)
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	s := imageSrc0Size()
	p := floor(srcPos - imageSrc0Origin())
	c := at(p.x, p.y)
	v := floor(c.r*255 + 0.5)
	if Sharp > 0 && p.x >= 1 && p.y >= 1 && p.x <= s.x-2 && p.y <= s.y-2 {
		a0 := lum(p.x-1, p.y-1)
		a1 := lum(p.x, p.y-1)
		a2 := lum(p.x+1, p.y-1)
		b0 := lum(p.x-1, p.y)
		b2 := lum(p.x+1, p.y)
		c0 := lum(p.x-1, p.y+1)
		c1 := lum(p.x, p.y+1)
		c2 := lum(p.x+1, p.y+1)
		blur := floor((a0 + 2*a1 + a2 + 2*b0 + 4*v + 2*b2 + c0 + 2*c1 + c2 + 8) / 16)
		d := v - blur
		if d > 2 || d < -2 {
			d -= 2 * sign(d)
			lo := min(min(min(v, a0), min(a1, a2)), min(min(b0, b2), min(min(c0, c1), c2)))
			hi := max(max(max(v, a0), max(a1, a2)), max(max(b0, b2), max(max(c0, c1), c2)))
			t := d * Sharp / 50
			sv := v + sign(t)*floor(abs(t))
			v = min(min(255, hi+12), max(max(0, lo-12), sv))
		}
	}
	g := clampColor(255 * pow(v/255, Gamma))
	if Mono > 0 {
		return vec4(floor(g*Tint/255)/255, 1)
	}
	cb := floor(c.g*255+0.5) - 128
	cr := floor(c.b*255+0.5) - 128
	rgb := vec3(clampColor(g+1.402*cr), clampColor(g-0.344136*cb-0.714136*cr), clampColor(g+1.772*cb))
	if Filter > 0 {
		l := 0.299*rgb.r + 0.587*rgb.g + 0.114*rgb.b
		m := clampColor((l-128)*FContrast + 128)
		rgb = vec3(clampColor(m*(1-FStrength)+FTint.x*(m/255)*FStrength),
			clampColor(m*(1-FStrength)+FTint.y*(m/255)*FStrength),
			clampColor(m*(1-FStrength)+FTint.z*(m/255)*FStrength))
	}
	return vec4(rgb/255, 1)
}
`

const lightShaderSrc = `//kage:unit pixels

package main

var Light float   // 1: the light map's gains
var Stretch float // 1: the auto levels after them
var Lo float      // their black point
var K float       // their gain
var Low float     // exposeLow
var GW float      // the light map's cells across and down
var GH float
var Holo float // 1: the hologram
var Band float // its band's last row
var BH float   // the band's height
var Ab float   // red and blue this far apart
var BandGain float
var Contrast float
var Sat float
var Tone float
var HTint vec3

func aux(x, y float) vec4 {
	return imageSrc1At(imageSrc1Origin() + vec2(x+0.5, y+0.5))
}

func gainCell(x, y float) float {
	t := aux(x, y)
	return (floor(t.r*255+0.5)*256 + floor(t.g*255+0.5)) / 65535 * 4
}

func gainAt(x, y float) float {
	s := imageSrc0Size()
	fy := (y+0.5)*GH/s.y - 0.5
	y0 := clamp(floor(fy), 0, GH-1)
	y1 := min(GH-1, y0+1)
	ty := clamp(fy-y0, 0, 1)
	fx := (x+0.5)*GW/s.x - 0.5
	x0 := clamp(floor(fx), 0, GW-1)
	x1 := min(GW-1, x0+1)
	tx := clamp(fx-x0, 0, 1)
	return (gainCell(x0, y0)*(1-tx)+gainCell(x1, y0)*tx)*(1-ty) + (gainCell(x0, y1)*(1-tx)+gainCell(x1, y1)*tx)*ty
}

func lit(x, y float) vec3 {
	s := imageSrc0Size()
	cx := clamp(x, 0, s.x-1)
	v := floor(imageSrc0At(imageSrc0Origin()+vec2(cx+0.5, y+0.5)).rgb*255 + 0.5)
	if Light > 0 {
		v = floor(clamp(v*gainAt(cx, y)+0.5, 0, 255))
		if Stretch > 0 {
			v = floor(clamp(Low+(v-Lo)*K+0.5, 0, 255))
		}
	}
	return v
}

func curve(c float) float {
	x := c / 255
	return 255 * ((1-Contrast)*x + Contrast*x*x*(3-2*x))
}

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := floor(srcPos - imageSrc0Origin())
	if Holo <= 0 {
		return vec4(lit(p.x, p.y)/255, 1)
	}
	sh := floor(aux(32, p.y).r*255+0.5) - 128
	k := 1.0
	d := p.y - Band + BH
	if d >= 0 && d < BH {
		k = BandGain
	}
	r := curve(lit(p.x+Ab+sh, p.y).r) * k
	g := curve(lit(p.x+sh, p.y).g) * k
	b := curve(lit(p.x-Ab+sh, p.y).b) * k
	l := 0.299*r + 0.587*g + 0.114*b
	r = l + (r-l)*Sat
	g = l + (g-l)*Sat
	b = l + (b-l)*Sat
	out := vec3(r, g, b)*(1-Tone) + l*HTint*Tone
	return vec4(floor(clamp(out+0.5, 0, 255))/255, 1)
}
`

var gpuVideo struct {
	once         sync.Once
	video, light *ebiten.Shader
	off          bool // the shaders did not compile: the CPU draws
}

func gpuShaders() bool {
	gpuVideo.once.Do(func() {
		var err error
		if gpuVideo.video, err = ebiten.NewShader([]byte(videoShaderSrc)); err == nil {
			gpuVideo.light, err = ebiten.NewShader([]byte(lightShaderSrc))
		}
		if err != nil {
			log.Printf("[gui] the video shaders: %v; the CPU draws the video", err)
			gpuVideo.off = true
		}
	})
	return !gpuVideo.off
}

// gpuAux is the aux image's region the CPU writes each frame: the light
// map's gains (a cell a texel, 16 bits in red and green) in columns 0-31,
// the hologram's row shifts (red, 128 none) in column 32.
const gpuAux = exposeGrid + 1

// gpuPane is a pane's GPU images, its picture's size: the picture's planes
// (src), the aux values, the colours before the light (mid).
type gpuPane struct {
	src, aux, mid  *ebiten.Image
	w, h           int
	packed, auxPix []byte
	samples        []uint8 // the light's samples' colours (every other pixel and row)
}

func (gp *gpuPane) dispose() {
	for _, im := range []*ebiten.Image{gp.src, gp.aux, gp.mid} {
		if im != nil {
			im.Deallocate()
		}
	}
}

// drawGPU draws p into pi.img on the GPU, as rasterizeSource would on the
// CPU; false when it cannot (no shaders, an effect only the CPU draws, a
// picture too narrow for the aux values).
func drawGPU(in Input, pi *paneImage, p *codec.Pixels, tint color.RGBA) bool {
	holo := effectExposes.Load()
	if videoEffect.Load() != nil && !holo || p.W < gpuAux || p.H < 4 || !gpuShaders() {
		return false
	}
	w, h := p.W, p.H
	gp := pi.gpu
	if gp == nil || gp.w != w || gp.h != h {
		if gp != nil {
			gp.dispose()
		}
		opt := &ebiten.NewImageOptions{Unmanaged: true}
		gp = &gpuPane{w: w, h: h, packed: make([]byte, 4*w*h), auxPix: make([]byte, 4*gpuAux*h),
			src: ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), opt),
			aux: ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), opt),
			mid: ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), opt)}
		pi.gpu = gp
	}
	n := w * h
	ys, cbs, crs, pk := p.Y[:n], p.Cb[:n], p.Cr[:n], gp.packed[:4*n]
	for i := range ys {
		pk[4*i], pk[4*i+1], pk[4*i+2], pk[4*i+3] = ys[i], cbs[i], crs[i], 255
	}
	gp.src.WritePixels(pk)

	mono := in.Mono != nil && in.Mono()
	fon, fc, fs, ft := ui.DisplayColorFilter()
	u := map[string]any{
		"Sharp": float32(videoSharp.Load()), "Gamma": float32(ui.DisplayGamma),
		"Mono": b2f(mono), "Tint": []float32{float32(tint.R), float32(tint.G), float32(tint.B)},
		"Filter": b2f(fon && !mono), "FContrast": float32(fc), "FStrength": float32(fs),
		"FTint": []float32{float32(ft[0]), float32(ft[1]), float32(ft[2])},
	}
	light := videoLight.Load() || holo
	first := pi.img
	if light || holo {
		first = gp.mid
	}
	first.DrawRectShader(w, h, gpuVideo.video, &ebiten.DrawRectShaderOptions{
		Images: [4]*ebiten.Image{gp.src}, Uniforms: u, Blend: ebiten.BlendCopy})
	if !light && !holo {
		return true
	}

	clear(gp.auxPix)
	for i := 3; i < len(gp.auxPix); i += 4 {
		gp.auxPix[i] = 255
	}
	lu := map[string]any{"Light": float32(0), "Holo": b2f(holo)}
	if light {
		gw, gh, gains, lo, k, stretch := gp.lightMap(in, p, tint)
		for cy := range gh {
			for cx := range gw {
				v := uint16(min(65535, max(0, math.Round(gains[cy*exposeGrid+cx]/4*65535))))
				o := 4 * (cy*gpuAux + cx)
				gp.auxPix[o], gp.auxPix[o+1] = byte(v>>8), byte(v)
			}
		}
		lu["Light"], lu["Stretch"], lu["Lo"], lu["K"], lu["Low"] = float32(1), b2f(stretch), float32(lo), float32(k), float32(exposeLow)
		lu["GW"], lu["GH"] = float32(gw), float32(gh)
	}
	for y := range h {
		gp.auxPix[4*(y*gpuAux+exposeGrid)] = 128
	}
	if holo {
		band, bh, ab, ht := hologramRows(w, h, tint, func(y, shift int) { gp.auxPix[4*(y*gpuAux+exposeGrid)] = byte(128 + shift) })
		lu["Band"], lu["BH"], lu["Ab"], lu["BandGain"] = float32(band), float32(bh), float32(ab), float32(hologramBand)
		lu["Contrast"], lu["Sat"], lu["Tone"] = float32(hologramContrast), float32(hologramSat), float32(hologramTone)
		lu["HTint"] = []float32{float32(ht[0]), float32(ht[1]), float32(ht[2])}
	}
	gp.aux.SubImage(image.Rect(0, 0, gpuAux, h)).(*ebiten.Image).WritePixels(gp.auxPix)
	pi.img.DrawRectShader(w, h, gpuVideo.light, &ebiten.DrawRectShaderOptions{
		Images: [4]*ebiten.Image{gp.mid, gp.aux}, Uniforms: lu, Blend: ebiten.BlendCopy})
	return true
}

// lightMap is applyLight's light map, gains and levels for the picture the
// video shader draws (its colours by rgbFunc, unsharpened), from every
// other pixel of every other row.
func (gp *gpuPane) lightMap(in Input, p *codec.Pixels, tint color.RGBA) (gw, gh int, gains [exposeGrid * exposeGrid]float64, lo, k float64, stretch bool) {
	const G = exposeGrid
	w, h := p.W, p.H
	gw, gh = min(G, w), min(G, h)
	rgbOf := rgbFunc(in, tint)
	// The luma as the video shader sharpens it (sharpenAt: at the samples
	// only).
	amount := int(videoSharp.Load())
	gp.samples = gp.samples[:0]
	var cells [G * G]float64
	var cnt [G * G]int
	var srcHist [256]int
	samples := 0
	for y := 0; y < h; y += 2 {
		cy := y * gh / h
		for x := 0; x < w; x += 2 {
			i := y*w + x
			r, g, b := rgbOf(sharpenAt(p.Y, w, h, x, y, amount), p.Cb[i], p.Cr[i])
			gp.samples = append(gp.samples, r, g, b)
			c := cy*G + x*gw/w
			l := (77*int(r) + 150*int(g) + 29*int(b)) >> 8
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
	gains = exposeGains(gp, cells, gw, gh)
	var hist [256]int
	s := gp.samples
	for y := 0; y < h; y += 2 {
		fy := (float64(y)+0.5)*float64(gh)/float64(h) - 0.5
		y0 := min(gh-1, max(0, int(math.Floor(fy))))
		y1, ty := min(gh-1, y0+1), min(1, max(0, fy-float64(y0)))
		for x := 0; x < w; x += 2 {
			fx := (float64(x)+0.5)*float64(gw)/float64(w) - 0.5
			x0 := min(gw-1, max(0, int(math.Floor(fx))))
			x1, tx := min(gw-1, x0+1), min(1, max(0, fx-float64(x0)))
			g := (gains[y0*G+x0]*(1-tx)+gains[y0*G+x1]*tx)*(1-ty) + (gains[y1*G+x0]*(1-tx)+gains[y1*G+x1]*tx)*ty
			r, gg, b := clamp8(float64(s[0])*g), clamp8(float64(s[1])*g), clamp8(float64(s[2])*g)
			hist[(77*int(r)+150*int(gg)+29*int(b))>>8]++
			s = s[3:]
		}
	}
	lo, k, stretch = exposeLevels(&hist, samples, &srcHist, samples)
	return gw, gh, gains, lo, k, stretch
}

// hologramRows moves the hologram's band as applyEffect does (both panes'
// together, by the clock) and draws its jumping rows (shift(y, s): row y
// jumps s pixels); it returns the band's row, its height, how far red and
// blue go apart and the pane's tint as gains.
func hologramRows(w, h int, tint color.RGBA, shift func(y, s int)) (band, bh, ab int, ht [3]float64) {
	effectMu.Lock()
	defer effectMu.Unlock()
	f := filters
	f.n++
	f.tint = paneTint(tint)
	now := time.Now()
	if !f.bandAt.IsZero() {
		dt := min(now.Sub(f.bandAt), 200*time.Millisecond)
		f.band = math.Mod(f.band+dt.Seconds()/bandPass().Seconds(), 1)
	}
	f.bandAt = now
	bh, jit := max(4, h/14), max(1, w/88)
	ab = max(1, w/176)
	band = int(f.band * float64(h+2*bh))
	for y := range h {
		if f.rng.IntN(hologramJitterEvery) == 0 {
			if s := f.rng.IntN(2*jit+1) - jit; s != 0 {
				shift(y, max(-127, min(127, s)))
			}
		}
	}
	return band, bh, ab, f.tint
}

func b2f(b bool) float32 {
	if b {
		return 1
	}
	return 0
}
