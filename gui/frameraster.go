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
