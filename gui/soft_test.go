package gui

import (
	"image/color"
	"os"
	"testing"
)

func testSoftFont(t *testing.T) *softFont {
	t.Helper()
	a, err1 := os.ReadFile("fonts/unifont.otf.zst")
	b, err2 := os.ReadFile("fonts/unifont_upper.otf.zst")
	if err1 != nil || err2 != nil {
		t.Skip("no fonts")
	}
	f, err := newSoftFont(16, a, b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Unifont's glyphs come out of the CPU rasteriser in their cell: a Latin
// letter, a Cyrillic one, a wide Chinese one, an emoji from Unifont Upper.
func TestSoftFontGlyphs(t *testing.T) {
	f := testSoftFont(t)
	for _, s := range []string{"A", "Ж", "中", "✅"} {
		g := f.glyph(s)
		if g == nil || g.covered() < 10 {
			t.Fatalf("%q: no glyph", s)
		}
		b := g.mask.Bounds().Add(g.at)
		if b.Min.Y < 0 || b.Max.Y > 16 || b.Min.X < 0 || b.Max.X > 16 {
			t.Fatalf("%q: glyph at %v, outside its cells", s, b)
		}
	}
}

// The renderer draws backgrounds, shapes and text, and then redraws only
// what changed.
func TestSoftRendererDrawsAndRedrawsChanges(t *testing.T) {
	r := newSoftRenderer(testSoftFont(t), 8, 16)
	red := color.RGBA{255, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}
	cells := make([]Cell, 4*2)
	for i := range cells {
		cells[i] = Cell{Bg: color.RGBA{0, 0, 0, 255}, Fg: white}
	}
	cells[0].Text = "A"
	cells[1] = Cell{Bg: red, Fg: white, Shape: true, Pattern: 0xffffffff} // a full block
	dirty := r.draw(4, 2, cells)
	if len(dirty) != 2 || dirty[0].Dx() != 32 {
		t.Fatalf("first frame: %v, want both rows whole", dirty)
	}
	px := func(x, y int) (b, g, rr byte) {
		o := (y*r.w + x) * softBytes
		return r.pix[o], r.pix[o+1], r.pix[o+2]
	}
	if b, g, rr := px(12, 8); rr != 255 || g != 255 || b != 255 {
		t.Fatalf("the block is %d,%d,%d, want white", rr, g, b)
	}
	lit := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 8; x++ {
			if _, g, _ := px(x, y); g > 127 {
				lit++
			}
		}
	}
	if lit < 10 {
		t.Fatal("no A in the first cell")
	}
	if d := r.draw(4, 2, cells); len(d) != 0 {
		t.Fatalf("nothing changed, redrew %v", d)
	}
	cells[6].Text = "z"
	if d := r.draw(4, 2, cells); len(d) != 1 || d[0].Min.X != 16 || d[0].Dx() != 8 || d[0].Min.Y != 16 {
		t.Fatalf("one cell changed, redrew %v", d)
	}
}
