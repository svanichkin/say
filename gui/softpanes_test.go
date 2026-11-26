package gui

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/svanichkin/say/codec"
)

var softPreview = flag.String("softpreview", "", "write the software-rendered window to this PNG")

// testFrame is a picture of cols x rows cells: a colour gradient in the
// codec's glyphs.
func testFrame(cols, rows int, hue byte) *codec.TextFrame {
	n := cols * rows
	f := &codec.TextFrame{Cols: cols, Rows: rows, Width: cols * 4, Height: rows * 8,
		Glyphs: make([]byte, n), FgYCbCr: make([]byte, 3*n), BgYCbCr: make([]byte, 3*n)}
	glyphs := len(codec.GlyphCodepoints())
	for i := 0; i < n; i++ {
		x, y := i%cols, i/cols
		f.Glyphs[i] = byte((x + y) % glyphs)
		f.BgYCbCr[3*i], f.BgYCbCr[3*i+1], f.BgYCbCr[3*i+2] = byte(40+y*180/rows), hue, byte(255-int(hue))
		f.FgYCbCr[3*i], f.FgYCbCr[3*i+1], f.FgYCbCr[3*i+2] = byte(60+x*180/cols), byte(255-int(hue)), hue
	}
	return f
}

// The window as Plan 9 draws it: the cells, the pictures in their frames,
// the HUD over them, the captions on their plates.
func TestSoftPixelsComposeTheWindow(t *testing.T) {
	font := testSoftFont(t)
	frames := map[uint64]*codec.TextFrame{1: testFrame(80, 40, 90), 2: testFrame(80, 40, 200)}
	in := Input{
		Frame: func(id uint64) *codec.TextFrame { return frames[id] },
		FrameRGB: func(y, cb, cr byte) (r, g, b byte) {
			c := color.YCbCr{Y: y, Cb: cb, Cr: cr}
			r32, g32, b32, _ := c.RGBA()
			return byte(r32 >> 8), byte(g32 >> 8), byte(b32 >> 8)
		},
		Accents: func() (self, peer [3]int) { return [3]int{0, 255, 255}, [3]int{255, 64, 160} },
		Chat:    func() [3]int { return [3]int{200, 90, 255} },
		Seeds:   func() (uint64, uint64, uint64) { return 11, 22, 33 },
	}
	s := NewScreen(96, 40)
	fmt.Fprint(s, "\x1b]5577;4;7;40;20;1;L\x07\x1b]5577;4;51;40;20;2;R\x07")
	fmt.Fprint(s, "\x1b]5578;4;7;30fps TX 12kBps\x07\x1b]5578;4;51;29fps RX 14kBps\x07")
	fmt.Fprint(s, "\x1b]5579;28;7;84;10\x07\x1b[29;9H\x1b[38;2;0;255;255mhello from Plan 9\x1b[0m")
	fmt.Fprint(s, "\x1b]5580;25;7;84;0;14;40;44;70;84\x07")

	base := newSoftRenderer(font, 8, 16)
	px := newSoftPixels(in, font, 8, 16)
	var dirty []image.Rectangle
	brackets := s.Brackets() // not under View: both take the screen's lock
	s.View(func(cols, rows int, cells []Cell, panes []Pane, _ uint64) {
		d := base.draw(cols, rows, cells)
		dirty = px.update(base, append([]Pane(nil), panes...), brackets, d)
	})
	if len(dirty) == 0 || dirty[0] != image.Rect(0, 0, 96*8, 40*16) {
		t.Fatalf("the first frame: %v, want the whole window", dirty)
	}
	at := func(x, y int) (r, g, b byte) {
		o := (y*px.w + x) * softBytes
		return px.out[o+2], px.out[o+1], px.out[o]
	}
	// The middle of each pane shows its picture (not black), the space
	// between the panes stays dark.
	for _, p := range [][2]int{{(6 + 20) * 8, (3 + 10) * 16}, {(50 + 20) * 8, (3 + 10) * 16}} {
		if r, g, b := at(p[0], p[1]); int(r)+int(g)+int(b) < 60 {
			t.Fatalf("no picture at %v: %d,%d,%d", p, r, g, b)
		}
	}
	if r, g, b := at(48*8, 3*16+8); int(r)+int(g)+int(b) > 120 {
		t.Fatalf("between the panes %d,%d,%d, want dark", r, g, b)
	}
	// A new picture recomposes its pane only.
	frames[1] = testFrame(80, 40, 30)
	s.View(func(cols, rows int, cells []Cell, panes []Pane, _ uint64) {
		d := base.draw(cols, rows, cells)
		dirty = px.update(base, append([]Pane(nil), panes...), brackets, d)
	})
	if len(dirty) != 1 || dirty[0] != image.Rect(6*8, 3*16, 46*8, 23*16) {
		t.Fatalf("a new picture redrew %v, want its pane", dirty)
	}
	if *softPreview != "" {
		img := image.NewRGBA(image.Rect(0, 0, px.w, px.h))
		for y := 0; y < px.h; y++ {
			for x := 0; x < px.w; x++ {
				r, g, b := at(x, y)
				img.Set(x, y, color.RGBA{r, g, b, 255})
			}
		}
		f, err := os.Create(*softPreview)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		png.Encode(f, img)
	}
}

// A text window (the chat, the status) has the grey dots on its
// background, to its inner line; outside it there are none.
func TestSoftPixelsDotTheTextWindows(t *testing.T) {
	font := testSoftFont(t)
	in := Input{Chat: func() [3]int { return [3]int{200, 90, 255} }}
	s := NewScreen(60, 20)
	fmt.Fprint(s, "\x1b]5579;6;10;30;6;c\x07")
	base := newSoftRenderer(font, 8, 16)
	px := newSoftPixels(in, font, 8, 16)
	s.View(func(cols, rows int, cells []Cell, panes []Pane, _ uint64) {
		px.update(base, append([]Pane(nil), panes...), nil, base.draw(cols, rows, cells))
	})
	step, row, size := dotGrid(8)
	count := func(r image.Rectangle) (n int) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				o := (y*px.w + x) * softBytes
				if dotAt(x, y, step, row, size) && px.out[o] == dotGrey && px.out[o+1] == dotGrey && px.out[o+2] == dotGrey {
					n++
				}
			}
		}
		return n
	}
	if n := count(image.Rect(10*8, 6*16, 38*8, 11*16)); n < 100 {
		t.Fatalf("%d dots in the window", n)
	}
	if n := count(image.Rect(0, 15*16, 60*8, 20*16)); n != 0 {
		t.Fatalf("%d dots outside it", n)
	}
}

// A grey-framed menu column (inactive) has fainter dots than an active one.
func TestFaintDotsInInactiveColumns(t *testing.T) {
	if g := paneDotGrey(Pane{Menu: true, Colour: color.RGBA{70, 70, 76, 255}}); g != dotGreyFaint {
		t.Fatalf("an inactive column's dots: %d", g)
	}
	if g := paneDotGrey(Pane{Menu: true, Colour: color.RGBA{255, 128, 0, 255}}); g != dotGrey {
		t.Fatalf("an active column's dots: %d", g)
	}
	if g := paneDotGrey(Pane{Chat: true}); g != dotGrey {
		t.Fatalf("the status's dots: %d", g)
	}
}
