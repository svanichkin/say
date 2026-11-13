//go:build darwin || windows

package gui

import (
	"math"
	"reflect"
	"testing"
)

func TestFrameContoursFitSmallAndWidePanes(t *testing.T) {
	g := &game{cw: 8, ch: 16}
	for seed := uint64(0); seed < 200; seed++ {
		for _, size := range [][2]int{{4, 2}, {10, 3}, {30, 15}, {90, 6}, {12, 30}} {
			for _, remote := range []bool{false, true} {
				p := Pane{Row: 3, Col: 3, Cols: size[0], Rows: size[1], Remote: remote}
				f := g.frameOf(p, "29fps RX 12ms", generate(seed, false))
				x, y, w, h := float64(p.Col*g.cw), float64(p.Row*g.ch), float64(p.Cols*g.cw), float64(p.Rows*g.ch)
				for _, q := range f.Inner {
					if q[0] < x || q[0] > x+w || q[1] < y || q[1] > y+h {
						t.Fatalf("seed %d size %v: inner contour outside pane: %v", seed, size, q)
					}
				}
				for _, poly := range [][]pt{f.Inner, f.Outer} {
					for i, a := range poly {
						if math.IsNaN(a[0]) || math.IsNaN(a[1]) {
							t.Fatal("non-finite contour")
						}
						b := poly[(i+1)%len(poly)]
						for j := i + 2; j < len(poly); j++ {
							if i == 0 && j == len(poly)-1 {
								continue
							}
							if frameEdgesCross(a, b, poly[j], poly[(j+1)%len(poly)]) {
								t.Fatalf("seed %d size %v: self-intersecting contour", seed, size)
							}
						}
					}
				}
				for _, q := range append(append([]pt{}, f.Outer...), f.Nodes...) {
					if q[0] < x-2*float64(g.cw) || q[0] > x+w+2*float64(g.cw) || q[1] < y-2*float64(g.cw) || q[1] > y+h+2*float64(g.cw) {
						t.Fatalf("seed %d: ornament leaves the gutter: %v", seed, q)
					}
				}
			}
		}
	}
}

func frameEdgesCross(a, b, c, d pt) bool {
	cross := func(a, b, c pt) float64 { return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]) }
	return cross(a, b, c)*cross(a, b, d) < -1e-8 && cross(c, d, a)*cross(c, d, b) < -1e-8
}

func TestFrameGeometryCacheTracksLayoutNotVideo(t *testing.T) {
	seed := uint64(100)
	g := &game{cw: 8, ch: 16, in: Input{Seeds: func() (uint64, uint64, uint64) { return seed, 2, 3 }}}
	p := Pane{Row: 2, Col: 2, Cols: 30, Rows: 12, Caption: "30fps"}
	a := g.paneFrame(p)
	p.Frame++
	b := g.paneFrame(p)
	if &a.Outer[0] != &b.Outer[0] {
		t.Fatal("new video frame rebuilt unchanged geometry")
	}
	p.Cols++
	c := g.paneFrame(p)
	if reflect.DeepEqual(a.Outer, c.Outer) {
		t.Fatal("resize retained old geometry")
	}
	seed++
	d := g.paneFrame(p)
	if reflect.DeepEqual(c.Outer, d.Outer) {
		t.Fatal("new seed retained old geometry")
	}
}

func BenchmarkCachedPaneFrame(b *testing.B) {
	g := &game{cw: 16, ch: 32}
	p := Pane{Row: 2, Col: 2, Cols: 44, Rows: 22, Caption: "30fps TX 12kBps"}
	g.paneFrame(p)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Frame++
		g.paneFrame(p)
	}
}
