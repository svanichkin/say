package gui

import (
	"image/color"

	"github.com/svanichkin/say/codec"
)

// rasterizeFrame draws a frame's cells into pix (stride w pixels): each cell's
// glyph shape in its foreground over its background. With -bw every frame,
// and otherwise a black and white one (the sender dropped colour on a lossy
// path), shows in tint, brightness scaling the colour.
func rasterizeFrame(in Input, f *codec.TextFrame, pix []byte, w int, tint color.RGBA) {
	rasterizeCells(in, f, pix, w, tint)
	// The exposure first (-video-light; the hologram's own), then the effect.
	applyLight(pix, w, f.Rows*cellH)
	applyEffect(pix, w, f.Rows*cellH, tint) // -video-fx, toned by the pane's frame
}

// rasterizeCells draws the frame itself (rasterizeFrame), the pixels when
// it carries them.
func rasterizeCells(in Input, f *codec.TextFrame, pix []byte, w int, tint color.RGBA) {
	if p := f.Pixels; p != nil && p.W > 0 && p.H > 0 {
		rasterizePixels(in, p, pix, w, f.Rows*cellH, tint)
		return
	}
	glyphs := codec.GlyphCodepoints()
	total := f.Cols * f.Rows
	if len(f.Glyphs) < total || len(f.FgYCbCr) < total*3 || len(f.BgYCbCr) < total*3 {
		return
	}
	rgbOf := in.FrameRGB
	if (in.Mono != nil && in.Mono()) || monochrome(f, total) {
		rgbOf = func(y, _, _ byte) (byte, byte, byte) {
			if in.FrameY != nil {
				y = in.FrameY(y)
			}
			return byte(int(y) * int(tint.R) / 255), byte(int(y) * int(tint.G) / 255), byte(int(y) * int(tint.B) / 255)
		}
	}
	for i := 0; i < total; i++ {
		var pattern uint32
		if gi := int(f.Glyphs[i]); gi < len(glyphs) {
			pattern, _ = codec.GlyphPattern(glyphs[gi])
		}
		br, bgc, bb := rgbOf(f.BgYCbCr[i*3], f.BgYCbCr[i*3+1], f.BgYCbCr[i*3+2])
		fr, fgc, fb := br, bgc, bb
		if pattern != 0 {
			fr, fgc, fb = rgbOf(f.FgYCbCr[i*3], f.FgYCbCr[i*3+1], f.FgYCbCr[i*3+2])
		}
		x0, y0 := (i%f.Cols)*cellW, (i/f.Cols)*cellH
		for k := 0; k < 32; k++ {
			o := ((y0+k/cellW)*w + x0 + k%cellW) * 4
			if pattern&(1<<uint(31-k)) != 0 {
				pix[o], pix[o+1], pix[o+2] = fr, fgc, fb
			} else {
				pix[o], pix[o+1], pix[o+2] = br, bgc, bb
			}
			pix[o+3] = 255
		}
	}
}

// rasterizePixels draws a picture in pixels (video from Babe's smooth
// coder) over the pane's w x h, bilinear between its pixels' centres, in
// the window's colours (with -bw in tint, as the cells).
func rasterizePixels(in Input, p *codec.Pixels, pix []byte, w, h int, tint color.RGBA) {
	rgbOf := in.FrameRGB
	if in.Mono != nil && in.Mono() {
		rgbOf = func(y, _, _ byte) (byte, byte, byte) {
			if in.FrameY != nil {
				y = in.FrameY(y)
			}
			return byte(int(y) * int(tint.R) / 255), byte(int(y) * int(tint.G) / 255), byte(int(y) * int(tint.B) / 255)
		}
	}
	if rgbOf == nil {
		rgbOf = func(y, cb, cr byte) (byte, byte, byte) { return color.YCbCrToRGB(y, cb, cr) }
	}
	// Source coordinates in 1/256 of a pixel, centres aligned.
	sx := func(x int) (int, int, int) {
		v := max(0, ((2*x+1)*p.W*256/(2*w))-128)
		i := min(p.W-1, v>>8)
		return i, min(p.W-1, i+1), v & 255
	}
	xs := make([][3]int, w)
	for x := range w {
		i0, i1, t := sx(x)
		xs[x] = [3]int{i0, i1, t}
	}
	lerp := func(pl []byte, r0, r1, ty int, xi [3]int) byte {
		a := int(pl[r0+xi[0]])*(256-xi[2]) + int(pl[r0+xi[1]])*xi[2]
		b := int(pl[r1+xi[0]])*(256-xi[2]) + int(pl[r1+xi[1]])*xi[2]
		return byte((a*(256-ty) + b*ty + 1<<15) >> 16)
	}
	// Sharpened at the picture's own size (-video-sharp), a copy.
	py, done := sharpened(p.Y, p.W, p.H)
	defer done()
	for y := range h {
		v := max(0, ((2*y+1)*p.H*256/(2*h))-128)
		j0 := min(p.H-1, v>>8)
		r0, r1, ty := j0*p.W, min(p.H-1, j0+1)*p.W, v&255
		row := pix[y*w*4:]
		for x := range w {
			xi := xs[x]
			r, g, b := rgbOf(lerp(py, r0, r1, ty, xi), lerp(p.Cb, r0, r1, ty, xi), lerp(p.Cr, r0, r1, ty, xi))
			row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = r, g, b, 255
		}
	}
}

// monochrome reports whether a frame has no colour: its chroma is neutral
// in every colour drawn. A cell all background (or all foreground) keeps
// whatever its unused colour was before the sender went black and white.
func monochrome(f *codec.TextFrame, total int) bool {
	glyphs := codec.GlyphCodepoints()
	neutral := func(c []byte, i int) bool {
		cb, cr := c[i*3+1], c[i*3+2]
		return cb >= 126 && cb <= 130 && cr >= 126 && cr <= 130
	}
	for i := 0; i < total; i++ {
		var pattern uint32
		if gi := int(f.Glyphs[i]); gi < len(glyphs) {
			pattern, _ = codec.GlyphPattern(glyphs[gi])
		}
		if pattern != 0 && !neutral(f.FgYCbCr, i) {
			return false
		}
		if pattern != 0xffffffff && !neutral(f.BgYCbCr, i) {
			return false
		}
	}
	return true
}

// rasterizeSource draws a picture in pixels at its own size into pix (p.W
// x p.H), sharpened, exposed and with the effect, as rasterizeFrame does
// over the pane: the window then draws it over the pane larger (the GPU's
// bilinear, as rasterizePixels): the CPU's work some a quarter of drawing
// it at the pane's size first.
func rasterizeSource(in Input, p *codec.Pixels, pix []byte, tint color.RGBA) {
	rgbOf := in.FrameRGB
	if in.Mono != nil && in.Mono() {
		rgbOf = func(y, _, _ byte) (byte, byte, byte) {
			if in.FrameY != nil {
				y = in.FrameY(y)
			}
			return byte(int(y) * int(tint.R) / 255), byte(int(y) * int(tint.G) / 255), byte(int(y) * int(tint.B) / 255)
		}
	}
	if rgbOf == nil {
		rgbOf = func(y, cb, cr byte) (byte, byte, byte) { return color.YCbCrToRGB(y, cb, cr) }
	}
	n := p.W * p.H
	py, done := sharpened(p.Y, p.W, p.H)
	defer done()
	ys, cbs, crs, out := py[:n], p.Cb[:n], p.Cr[:n], pix[:4*n]
	for i, y := range ys {
		r, g, b := rgbOf(y, cbs[i], crs[i])
		out[4*i], out[4*i+1], out[4*i+2], out[4*i+3] = r, g, b, 255
	}
	applyLight(pix, p.W, p.H)
	applyEffect(pix, p.W, p.H, tint)
}

// hasPixels reports whether a frame carries a whole picture in pixels.
func hasPixels(f *codec.TextFrame) bool {
	p := f.Pixels
	return p != nil && p.W > 0 && p.H > 0 && len(p.Y) >= p.W*p.H && len(p.Cb) >= p.W*p.H && len(p.Cr) >= p.W*p.H
}
