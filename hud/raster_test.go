package hud

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"testing"
)

var previewPath = flag.String("preview", "", "write the rendered test scene, on black, to this PNG")

// testScene is a call's screen at 96x45 cells of 16x32 pixels.
func testScene() (Scene, Grid, int, int) {
	gr := Grid{CW: 16, CH: 32}
	sc := Scene{
		Panes: []Pane{
			{Rect: Rect{Row: 2, Col: 5, Cols: 40, Rows: 18}, Colour: color.RGBA{0, 255, 255, 255}, Seed: 11},
			{Rect: Rect{Row: 2, Col: 51, Cols: 40, Rows: 18}, Remote: true, Colour: color.RGBA{255, 64, 160, 255}, Seed: 22},
			{Rect: Rect{Row: 25, Col: 4, Cols: 88, Rows: 17}, Chat: true, Colour: color.RGBA{255, 230, 0, 255}, Seed: 33},
		},
		Brackets:   []Bracket{{Row: 21, Col: 5, Cols: 86, Spans: [][2]int{{2, 20}, {40, 46}, {66, 84}}}},
		Border:     true,
		BorderSeed: 11,
	}
	return sc, gr, 96 * 16, 45 * 32
}

func TestRenderDrawsTheHUDAndLeavesTheRestClear(t *testing.T) {
	sc, gr, w, h := testScene()
	img := Render(sc, gr, w, h)
	alpha := func(x, y int) uint8 { return img.RGBAAt(x, y).A }
	// The video's middle shows through; the border's bars do not.
	if a := alpha(5*16+20*16, 2*32+9*32); a != 0 {
		t.Fatalf("the middle of a pane is covered (alpha %d)", a)
	}
	if a := alpha(8, h/2-200); a < 200 {
		t.Fatalf("the left bar is missing (alpha %d)", a)
	}
	// Something is drawn on each pane's outline: its frame.
	for _, p := range sc.Panes {
		x := float64(gr.CW*p.Col) + float64(gr.CW*p.Cols)/2
		y := float64(gr.CH * p.Row)
		found := false
		for dy := -16; dy <= 2; dy++ {
			if alpha(int(x), int(y)+dy) > 100 {
				found = true
			}
		}
		if !found {
			t.Fatalf("no frame on top of the pane at %v", p.Rect)
		}
	}
	if *previewPath != "" {
		out := image.NewRGBA(img.Bounds())
		draw.Draw(out, out.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
		draw.Draw(out, out.Bounds(), img, image.Point{}, draw.Over)
		f, err := os.Create(*previewPath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkRender(b *testing.B) {
	sc, gr, w, h := testScene()
	for b.Loop() {
		Render(sc, gr, w, h)
	}
}
