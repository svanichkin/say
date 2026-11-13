package hud

import (
	"image/color"
	"math"
	"math/rand/v2"
)

// How the HUD is drawn: colours and line widths, the window's border and
// the brackets of a row. Everything is filled polygons; a stroke is a quad.

var (
	// HazardOrange is the border's and the brackets' orange.
	HazardOrange = color.RGBA{255, 140, 0, 255}
	// Background is the window's background, which cuts stripes into plates.
	Background = color.RGBA{0, 0, 0, 255}
	// BarGlow is the colour the border's bars glow in.
	BarGlow = color.RGBA{110, 60, 0, 255}
	// HazardDark is the dark between the orange stripes.
	HazardDark = color.RGBA{10, 10, 10, 255}
)

// Widths are a frame's line widths for cells u wide: the inner line (the
// video's edge), the outer one and the circuit traces.
func Widths(u float64) (inner, outer, trace float64) {
	return math.Max(1, u/10), math.Max(1.5, u/7), math.Max(1, u/12)
}

// Node is the diamond at the end of a circuit trace.
func Node(at Pt, u float64) []Pt {
	d := u * 0.16
	return []Pt{{at[0] - d, at[1]}, {at[0], at[1] - d}, {at[0] + d, at[1]}, {at[0], at[1] + d}}
}

// Dimmer is the colour of a frame's inner line and rails.
func Dimmer(c color.RGBA) color.RGBA {
	return color.RGBA{c.R / 3 * 2, c.G / 3 * 2, c.B / 3 * 2, 255}
}

// LabelFill is the colour of a caption's plate: dark, the caption glows on it.
func LabelFill(c color.RGBA) color.RGBA {
	return color.RGBA{c.R / 5, c.G / 5, c.B / 5, 255}
}

// Line is a straight stroke from a to c, width wide.
func Line(a, c Pt, width float64) []Pt {
	dx, dy := c[0]-a[0], c[1]-a[1]
	n := math.Hypot(dx, dy)
	if n == 0 {
		return nil
	}
	nx, ny := -dy/n*width/2, dx/n*width/2
	return []Pt{{a[0] + nx, a[1] + ny}, {c[0] + nx, c[1] + ny}, {c[0] - nx, c[1] - ny}, {a[0] - nx, a[1] - ny}}
}

// Loop is a closed polyline through ps, width wide.
func Loop(ps []Pt, width float64) [][]Pt {
	out := make([][]Pt, 0, len(ps))
	for i := range ps {
		if q := Line(ps[i], ps[(i+1)%len(ps)], width); q != nil {
			out = append(out, q)
		}
	}
	return out
}

// HazardIsOrange reports whether a hazard texture's pixel at x, y is in an
// orange stripe: stripes stripe wide, slanting at 45 degrees.
func HazardIsOrange(x, y, stripe int) bool {
	return (x+y)/max(2, stripe)%2 == 0
}

// Border is the window's own border (w x h): on the left and on the right
// a bar of hazard stripes, its ends cut off and a step cut into its inner
// edge as the frames have; along the top and the bottom a thin orange line
// with plates on it, some striped.
type Border struct {
	Bars [2][]Pt // left and right, filled with the hazard stripes
	// BarX0 is where each bar's stripes start: a bar's pixel at x, y takes
	// the hazard texture's at x-BarX0+1, y+1.
	BarX0  [2]float64
	BarW   float64
	Lines  [2][2]Pt // the top and the bottom line, LineW wide
	LineW  float64
	Plates [][]Pt // plain plates on the lines
	// Striped plates take the hazard texture's pixel at x+1,
	// mod(y, StripedModY)+1.
	Striped     [][]Pt
	StripedModY float64
	Stripe      int // the stripes' width
}

// BorderOf lays out the border of a window w x h for cells cw wide; where
// the plates go follows from seed. It reports false for a window too small.
func BorderOf(w, h, cw int, seed uint64) (Border, bool) {
	u := float64(cw)
	bw := 1.4 * u // the bars' width
	// Flush with the window's edges: a pixel past them, so that the
	// antialiased outer edge falls outside and no dark seam is left.
	m := -1.0
	line := 0.2*u + 1 // the top and bottom lines' thickness
	W, H := float64(w), float64(h)
	if W < 8*u || H < 8*u {
		return Border{}, false
	}
	b := Border{BarW: bw, LineW: line, Stripe: cw * 3 / 5}
	top, bottom := m, H-m
	length := bottom - top
	d := bw * 0.45 // how deep the step is
	s1, s2 := top+length*0.3, top+length*0.7
	left := []Pt{
		{m, top + bw}, {m + bw, top},
		{m + bw, s1}, {m + bw - d, s1 + d}, {m + bw - d, s2 - d}, {m + bw, s2},
		{m + bw, bottom}, {m, bottom - bw},
	}
	right := make([]Pt, len(left))
	for i, q := range left {
		right[len(left)-1-i] = Pt{W - q[0], q[1]}
	}
	b.Bars = [2][]Pt{left, right}
	b.BarX0 = [2]float64{m, W - m - bw}
	// Along the top and the bottom: a line with plates on it, as the frames
	// have, some striped.
	x0, x1 := m+bw+u, W-m-bw-u
	depth := u
	b.StripedModY = depth + line
	rng := rand.New(rand.NewPCG(seed, 0xb0d3))
	for k, y := range []float64{m + line/2, H - m - line/2} {
		b.Lines[k] = [2]Pt{{x0, y}, {x1, y}}
		dir := 1.0 // the plates hang into the window
		if k == 1 {
			dir = -1
		}
		edge := y + dir*line/2
		for _, pl := range edgePlates(rng, x1-x0) {
			a, c := x0+pl.from, x0+pl.to
			d := depth * pl.depth
			ps := []Pt{{a, edge}, {c, edge}, {c - d, edge + dir*d}, {a + d, edge + dir*d}}
			if pl.striped {
				b.Striped = append(b.Striped, ps)
			} else {
				b.Plates = append(b.Plates, ps)
			}
		}
	}
	return b, true
}

// edgePlate is a plate on the top or bottom line: from..to along it, its
// depth a fraction of the most, plain or striped.
type edgePlate struct {
	from, to, depth float64
	striped         bool
}

// edgePlates lays three to five plates along a line length long, apart
// from each other, one of them at least striped.
func edgePlates(rng *rand.Rand, length float64) []edgePlate {
	n := 3 + rng.IntN(3)
	slot := length / float64(n)
	var out []edgePlate
	for i := range n {
		w := slot * (0.3 + rng.Float64()*0.4)
		from := float64(i)*slot + (slot-w)*rng.Float64()
		out = append(out, edgePlate{from: from, to: from + w, depth: 0.6 + rng.Float64()*0.4, striped: rng.IntN(3) == 0})
	}
	out[rng.IntN(n)].striped = true
	return out
}

// Bracket is a row bracketed: a block at each end and a line between the
// pieces of text, which span columns [from, to) of the row's Cols (cells,
// 0-based Row and Col).
type Bracket struct {
	Row, Col, Cols int
	Spans          [][2]int
}

// BracketShapes are a bracket's blocks and lines, in HazardOrange: the
// lines as thin as the frames' outlines, in the middle of the row.
func BracketShapes(gr Grid, b Bracket) [][]Pt {
	if b.Cols < 4 {
		return nil
	}
	u := float64(gr.CW)
	_, line, _ := Widths(u)
	x0 := float64(gr.OX + b.Col*gr.CW)
	x1 := x0 + float64(b.Cols*gr.CW)
	y := float64(gr.OY+b.Row*gr.CH) + float64(gr.CH)/2
	bw, bh := u*0.5, float64(gr.CH)*0.6 // an end block
	gap := u * 0.5                      // between a line and the text
	rect := func(ax, ay, bx, by float64) []Pt { return []Pt{{ax, ay}, {bx, ay}, {bx, by}, {ax, by}} }
	shapes := [][]Pt{
		rect(x0, y-bh/2, x0+bw, y+bh/2),
		rect(x1-bw, y-bh/2, x1, y+bh/2),
	}
	// A line from the left block to the first text, between the texts, and
	// from the last text to the right block; an empty span (no ping yet) is
	// passed over.
	from := x0 + bw
	for _, sp := range b.Spans {
		if sp[0] >= sp[1] || sp[0] < 1 || sp[1] > b.Cols-1 {
			continue
		}
		tFrom, tTo := x0+float64(sp[0])*u, x0+float64(sp[1])*u
		if tFrom-gap > from {
			shapes = append(shapes, rect(from, y-line/2, tFrom-gap, y+line/2))
		}
		from = tTo + gap
	}
	if from < x1-bw {
		shapes = append(shapes, rect(from, y-line/2, x1-bw, y+line/2))
	}
	return shapes
}
