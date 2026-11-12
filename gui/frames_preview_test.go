//go:build darwin || windows

package gui

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

var previewPath = flag.String("preview", "", "write a sheet of generated frames to this PNG")

// TestFramesPreview draws frames generated from several seeds (video panes
// in pairs, then a chat) into a PNG, without a window: go test -run
// FramesPreview -preview=frames.png ./gui/
func TestFramesPreview(t *testing.T) {
	if *previewPath == "" {
		t.Skip("pass -preview=<file.png> to draw the sheet")
	}
	const cw, ch = 16, 32
	const paneCols, paneRows = 30, 9
	pairs := 3
	W := (2*paneCols + 12) * cw
	H := pairs*(paneRows+6)*ch + 14*ch
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{8, 8, 12, 255}), image.Point{}, draw.Src)
	accents := []color.RGBA{{255, 128, 0, 255}, {0, 255, 255, 255}, {255, 64, 160, 255}, {0, 255, 64, 255}, {200, 90, 255, 255}, {255, 230, 0, 255}, {80, 150, 255, 255}, {255, 64, 64, 255}}
	g := &game{cw: cw, ch: ch}
	ttf, err := opentype.Parse(gomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: float64(cw) / 0.6, DPI: 72})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	plot := func(f paneFrame, c color.RGBA, video color.RGBA) {
		fillPoly(img, f.inner, video)
		for _, ps := range f.rails {
			fillPoly(img, ps, scale(c, 2, 3))
		}
		strokeLoop(img, f.inner, 1.6, scale(c, 2, 3))
		strokeLoop(img, f.outer, 2.3, c)
		for _, path := range f.traces {
			for i := 1; i < len(path); i++ {
				strokeLoop(img, []pt{path[i-1], path[i]}, 1.4, c)
			}
		}
		for _, node := range f.nodes {
			const d = 2.5
			fillPoly(img, []pt{{node[0] - d, node[1]}, {node[0], node[1] - d}, {node[0] + d, node[1]}, {node[0], node[1] + d}}, c)
		}
		for _, ps := range f.plates {
			fillPoly(img, ps, c)
		}
		for _, ps := range f.teeth {
			fillPoly(img, ps, c)
		}
		for _, ps := range f.cuts {
			fillPoly(img, ps, color.RGBA{8, 8, 12, 255})
		}
		if f.label != nil {
			fillPoly(img, f.label, c)
			d := font.Drawer{Dst: img, Src: image.NewUniform(color.RGBA{8, 8, 12, 255}), Face: face,
				Dot: fixed.P(int(f.labelX), int(f.labelY)+ch/2+(face.Metrics().Ascent-face.Metrics().Descent).Ceil()/2)}
			d.DrawString(f.caption)
		}
	}
	for i := 0; i < pairs; i++ {
		row := 2 + i*(paneRows+6)
		for k, remote := range []bool{false, true} {
			p := Pane{Row: row, Col: 3 + k*(paneCols+6), Cols: paneCols, Rows: paneRows, Remote: remote}
			seed := uint64(1000+i*7+k) * 2654435761
			caption := "29fps TX↑ A0Bps V3kBps"
			if remote {
				caption = "27fps RX↓ A312Bps V3kBps 12ms"
			}
			plot(g.frameOf(p, caption, styleOf(seed, false)), accents[(2*i+k)%len(accents)], color.RGBA{30, 40, 48, 255})
		}
	}
	chat := Pane{Row: 2 + pairs*(paneRows+6), Col: 3, Cols: 2*paneCols + 6, Rows: 9, Chat: true}
	plot(g.frameOf(chat, "", styleOf(777, true)), color.RGBA{255, 230, 0, 255}, color.RGBA{14, 14, 18, 255})
	f, err := os.Create(*previewPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func scale(c color.RGBA, num, den uint8) color.RGBA {
	return color.RGBA{c.R / den * num, c.G / den * num, c.B / den * num, 255}
}

func fillPoly(img *image.RGBA, ps []pt, c color.RGBA) {
	if len(ps) < 3 {
		return
	}
	b := img.Bounds()
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	r.MoveTo(float32(ps[0][0]), float32(ps[0][1]))
	for _, q := range ps[1:] {
		r.LineTo(float32(q[0]), float32(q[1]))
	}
	r.ClosePath()
	r.Draw(img, b, image.NewUniform(c), image.Point{})
}

func strokeLoop(img *image.RGBA, ps []pt, width float64, c color.RGBA) {
	for i := range ps {
		a, b := ps[i], ps[(i+1)%len(ps)]
		dx, dy := b[0]-a[0], b[1]-a[1]
		n := math.Hypot(dx, dy)
		if n == 0 {
			continue
		}
		nx, ny := -dy/n*width/2, dx/n*width/2
		fillPoly(img, []pt{{a[0] + nx, a[1] + ny}, {b[0] + nx, b[1] + ny}, {b[0] - nx, b[1] - ny}, {a[0] - nx, a[1] - ny}}, c)
	}
}
