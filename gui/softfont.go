package gui

import (
	"image"
	"sync"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// softFont draws characters for the software renderer (the window drawn by
// the CPU, as on Plan 9): Unifont, as the window elsewhere, at a whole
// multiple of its 8x16 pixels, each glyph rasterised once and kept.
type softFont struct {
	faces  []font.Face // Unifont, then Unifont Upper (beyond the BMP)
	ascent int
	mu     sync.Mutex
	glyphs map[string]*softGlyph
}

// softGlyph is a character's coverage, placed relative to its cell's top
// left corner.
type softGlyph struct {
	mask *image.Alpha
	at   image.Point
}

// newSoftFont parses the zstd-compressed fonts (Unifont first) for cells
// cellH pixels tall.
func newSoftFont(cellH int, compressed ...[]byte) (*softFont, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	f := &softFont{glyphs: map[string]*softGlyph{}}
	for _, z := range compressed {
		data, err := dec.DecodeAll(z, nil)
		if err != nil {
			return nil, err
		}
		otf, err := opentype.Parse(data)
		if err != nil {
			return nil, err
		}
		face, err := opentype.NewFace(otf, &opentype.FaceOptions{Size: float64(cellH), DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		f.faces = append(f.faces, face)
	}
	if len(f.faces) > 0 {
		f.ascent = f.faces[0].Metrics().Ascent.Round()
	}
	return f, nil
}

// glyph returns the character s (a cluster: its first rune is drawn) or nil
// when no font has it.
func (f *softFont) glyph(s string) *softGlyph {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.glyphs[s]; ok {
		return g
	}
	var g *softGlyph
	r := []rune(s)
	if len(r) > 0 {
		for _, face := range f.faces {
			if _, ok := face.GlyphAdvance(r[0]); !ok {
				continue
			}
			dr, mask, mp, _, ok := face.Glyph(fixed.P(0, f.ascent), r[0])
			if !ok || dr.Empty() {
				break
			}
			a := image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
			for y := 0; y < dr.Dy(); y++ {
				for x := 0; x < dr.Dx(); x++ {
					_, _, _, al := mask.At(mp.X+x, mp.Y+y).RGBA()
					a.Pix[y*a.Stride+x] = uint8(al >> 8)
				}
			}
			g = &softGlyph{mask: a, at: dr.Min}
			break
		}
	}
	f.glyphs[s] = g
	return g
}

// covered counts the glyph's inked pixels (for tests).
func (g *softGlyph) covered() int {
	n := 0
	for _, v := range g.mask.Pix {
		if v > 127 {
			n++
		}
	}
	return n
}
