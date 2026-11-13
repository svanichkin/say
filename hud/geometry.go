package hud

// Package hud is the geometry of the HUD the window draws round the video
// panes and the chat (and of its own border), in device pixels, with the
// colours and widths it is drawn in: the window (package gui) draws it with
// the GPU, a terminal that shows images gets it rasterized here.

import (
	"math"
	"math/rand/v2"
	"sort"
	"sync"
)

// HUD frames of the video panes and the chat. Each launch has new styles,
// salted with each person's address, so the two panes differ;
// the peer's is mirrored, its caption facing outwards. The chat has a style
// and a colour of its own.
//
// A frame is a double outline (the video's edge and a line just outside it)
// with cut corners; armour plates sit on stretches of the outer line, some
// cut with hazard stripes; small teeth stick out of it; the caption is
// written in dark letters on a bright plate under the bottom edge.

// Grid is the cell grid in device pixels: a cell's size and where the
// first cell starts.
type Grid struct{ CW, CH, OX, OY int }

// Rect is a pane's place on the grid, in cells (0-based).
type Rect struct{ Row, Col, Cols, Rows int }

// Pt is a point in device pixels.
type Pt = [2]float64

type Frame struct {
	Inner   []Pt   // the video's edge (and where it shows), clockwise
	Outer   []Pt   // the line just outside it, clockwise (the ping runs on it)
	Plates  [][]Pt // armour plates and corner wedges, in the accent colour
	Rails   [][]Pt // filled stretches between the two lines
	Cuts    [][]Pt // hazard stripes cut out of the plates (background colour)
	Teeth   [][]Pt
	Label   []Pt // the caption's plate (nil: no caption)
	LabelX  float64
	LabelY  float64 // top of the caption's row
	Caption string
	Traces  [][]Pt // open circuit lines outside the silhouette
	Nodes   []Pt   // endpoints of those lines
}

// side of a frame: things are laid along it.
type side int

const (
	top side = iota
	right
	bottom
	left
)

// Style describes a frame (see generate).
type Style struct {
	cuts   [4]float64 // corner cuts in cells: top left, top right, bottom right, bottom left
	plates []plate
	rails  []plate // stretches where the gap between the two lines is filled
	teeth  []tooth
	label  float64 // where the caption plate starts along the bottom (0..1); -1 centred, -2 none
	steps  [4][]edgeStep
	traces []plate
}

// A shallow change in the silhouette, with diagonal shoulders. Depth is
// in cell widths; positive is outward, negative is a recess in the gutter.
type edgeStep struct {
	from, to, depth float64
}

type plate struct {
	side     side
	from, to float64 // along the side, 0..1 (left to right, top to bottom)
	stripes  bool
}

type tooth struct {
	side side
	at   float64 // along the side, 0..1
}

// Generate builds a frame from a seed, by these rules:
//
//   - corners: a big diagonal pair (top left and bottom right, or the other
//     two) with the rest small, or all four medium;
//   - armour plates: 2 to 4 (3 to 4 for the chat), on the top more often than
//     elsewhere, each 18 to 40% of its side, clear of the corners and of one
//     another; the first on the top is cut with hazard stripes, a bottom one
//     sometimes too;
//   - teeth: one or two groups of 2 to 4 on free stretches;
//   - the caption: under the bottom edge on the left, in the middle or on
//     the right (the chat has none: the right half of its bottom stays
//     bare for the version written under it); bottom plates keep clear of it.
//
// The same seed gives the same frame throughout a launch, including resizes.
func Generate(seed uint64, chat bool) Style {
	r := rand.New(rand.NewPCG(seed, seed^0x5851f42d4c957f2d))
	big := []float64{2.5, 3}[r.IntN(2)]
	small := []float64{0.6, 1.2}[r.IntN(2)]
	var st Style
	switch r.IntN(3) {
	case 0:
		st.cuts = [4]float64{big, small, big, small}
	case 1:
		st.cuts = [4]float64{small, big, small, big}
	default:
		m := []float64{1.4, 2}[r.IntN(2)]
		st.cuts = [4]float64{m, m, m, m}
	}
	// Unequal chamfers make the silhouette asymmetric without cutting deeply
	// into the video. The chat uses smaller cuts to keep text clear.
	for i := range st.cuts {
		st.cuts[i] *= 0.65 + r.Float64()*0.6
		if chat {
			st.cuts[i] = min(st.cuts[i], 1.1)
		}
	}
	var used [4][][2]float64 // taken stretches of each side
	free := func(s side, a, b float64) bool {
		if a < 0.08 || b > 0.92 {
			return false
		}
		for _, u := range used[s] {
			if a < u[1]+0.04 && b > u[0]-0.04 {
				return false
			}
		}
		return true
	}
	take := func(s side, a, b float64) { used[s] = append(used[s], [2]float64{a, b}) }
	st.label = -2
	if chat {
		// The version is written under the right half of the bottom.
		take(bottom, 0.5, 1)
	} else {
		switch r.IntN(3) {
		case 0:
			st.label = 0.04
			take(bottom, 0.04, 0.72)
		case 1:
			st.label = -1
			take(bottom, 0.15, 0.85)
		default:
			st.label = 0.3
			take(bottom, 0.28, 0.96)
		}
	}
	// Three densities: armour, circuitry, or a mixture. All share the same
	// silhouette grammar instead of choosing from a handful of fixed frames.
	family := r.IntN(3)
	n := 2 + r.IntN(2)
	if family == 1 {
		n = 1
	}
	// Reserve silhouette changes before placing decoration, so plates and
	// circuit lines cannot intersect the sloping shoulders of a step.
	for i, count := 0, 2+r.IntN(3); i < count; i++ {
		s := side(r.IntN(4))
		length := 0.14 + r.Float64()*0.18
		a := 0.18 + r.Float64()*(0.64-length)
		if !free(s, a, a+length) {
			continue
		}
		d := 0.3 + r.Float64()*0.35
		if r.IntN(3) == 0 {
			d = -0.25
		}
		st.steps[s] = append(st.steps[s], edgeStep{a, a + length, d})
		take(s, a, a+length)
	}
	striped := false
	for tries := 0; len(st.plates) < n && tries < 60; tries++ {
		var s side
		switch w := r.IntN(20); {
		case w < 9:
			s = top
		case w < 13:
			s = left
		case w < 17:
			s = right
		default:
			s = bottom
		}
		length := 0.18 + r.Float64()*0.22
		a := 0.08 + r.Float64()*(0.84-length)
		if !free(s, a, a+length) {
			continue
		}
		take(s, a, a+length)
		pl := plate{side: s, from: a, to: a + length}
		if !striped {
			pl.stripes, striped = true, true
		} else if s == bottom && r.IntN(3) == 0 {
			pl.stripes = true
		}
		st.plates = append(st.plates, pl)
	}
	for i, rails := 0, 1+r.IntN(2); i < rails; i++ {
		s := side(r.IntN(4))
		length := 0.25 + r.Float64()*0.3
		a := 0.08 + r.Float64()*(0.84-length)
		if !free(s, a, a+length) {
			continue
		}
		take(s, a, a+length)
		st.rails = append(st.rails, plate{side: s, from: a, to: a + length})
	}
	for tries := 0; len(st.traces) < 1+family && tries < 30; tries++ {
		s := side(r.IntN(4))
		a := 0.12 + r.Float64()*0.5
		b := a + 0.12 + r.Float64()*0.18
		if !free(s, a, b) {
			continue
		}
		take(s, a, b)
		st.traces = append(st.traces, plate{side: s, from: a, to: b})
	}
	for g, groups := 0, 1+r.IntN(2); g < groups; g++ {
		count := 2 + r.IntN(3)
		span := float64(count) * 0.045
		for tries := 0; tries < 20; tries++ {
			s := side(r.IntN(4))
			a := 0.08 + r.Float64()*(0.84-span)
			if !free(s, a, a+span) {
				continue
			}
			take(s, a, a+span)
			for i := 0; i < count; i++ {
				st.teeth = append(st.teeth, tooth{side: s, at: a + float64(i)*0.045})
			}
			break
		}
	}
	for s := range st.steps {
		sort.Slice(st.steps[s], func(i, j int) bool { return st.steps[s][i].from < st.steps[s][j].from })
	}
	return st
}

var (
	styleMu    sync.Mutex
	styleCache = map[styleKey]Style{}
)

type styleKey struct {
	seed uint64
	chat bool
}

// StyleOf returns the frame generated from seed (cached).
func StyleOf(seed uint64, chat bool) Style {
	key := styleKey{seed, chat}
	styleMu.Lock()
	defer styleMu.Unlock()
	st, ok := styleCache[key]
	if !ok {
		st = Generate(seed, chat)
		styleCache[key] = st
	}
	return st
}

// chamfered returns a rectangle with its corners cut by c (top left, top
// right, bottom right, bottom left), clockwise from the top left.
func chamfered(x0, y0, x1, y1 float64, c [4]float64) []Pt {
	return []Pt{
		{x0, y0 + c[0]}, {x0 + c[0], y0},
		{x1 - c[1], y0}, {x1, y0 + c[1]},
		{x1, y1 - c[2]}, {x1 - c[2], y1},
		{x0 + c[3], y1}, {x0, y1 - c[3]},
	}
}

// steppedContour keeps all edge changes in the gutter. Video and chat
// clipping stays on the simpler inner polygon, clear of decorative notches.
func steppedContour(x0, y0, x1, y1 float64, c [4]float64, steps [4][]edgeStep, u float64) []Pt {
	w, h := x1-x0, y1-y0
	edge := func(s side, startCut, endCut float64) []Pt {
		length := w
		if s == left || s == right {
			length = h
		}
		var ps []Pt
		for _, step := range steps[s] {
			a, b, d := step.from*length, step.to*length, step.depth*u
			shoulder := math.Abs(d)
			if a < startCut+u/2 || b > length-endCut-u/2 || b-a < 2*shoulder+u {
				continue
			}
			for _, p := range []Pt{{a, 0}, {a + shoulder, d}, {b - shoulder, d}, {b, 0}} {
				switch s {
				case top:
					ps = append(ps, Pt{x0 + p[0], y0 - p[1]})
				case right:
					ps = append(ps, Pt{x1 + p[1], y0 + p[0]})
				case bottom:
					ps = append(ps, Pt{x0 + p[0], y1 + p[1]})
				case left:
					ps = append(ps, Pt{x0 - p[1], y0 + p[0]})
				}
			}
		}
		if s == bottom || s == left {
			Reverse(ps)
		}
		return ps
	}
	ps := []Pt{{x0, y0 + c[0]}, {x0 + c[0], y0}}
	ps = append(ps, edge(top, c[0], c[1])...)
	ps = append(ps, Pt{x1 - c[1], y0}, Pt{x1, y0 + c[1]})
	ps = append(ps, edge(right, c[1], c[2])...)
	ps = append(ps, Pt{x1, y1 - c[2]}, Pt{x1 - c[2], y1})
	ps = append(ps, edge(bottom, c[3], c[2])...)
	ps = append(ps, Pt{x0 + c[3], y1}, Pt{x0, y1 - c[3]})
	return append(ps, edge(left, c[0], c[3])...)
}

// FrameOf lays out the frame of the pane (or chat) at p in device pixels;
// remote mirrors it (the peer's), so that its caption faces outwards.
func FrameOf(gr Grid, p Rect, remote bool, caption string, st Style) Frame {
	u := float64(gr.CW)
	x, y := float64(gr.OX+p.Col*gr.CW), float64(gr.OY+p.Row*gr.CH)
	w, h := float64(p.Cols*gr.CW), float64(p.Rows*gr.CH)
	gap, thick := 0.5*u, min(u, w/10, h/6)
	var cin, cout [4]float64
	for i, c := range st.cuts {
		cin[i] = min(c*u, min(w, h)*0.22)
		cout[i] = cin[i] + gap*0.41 // the outer line keeps the cuts' width
	}
	f := Frame{
		Inner:   chamfered(x, y, x+w, y+h, cin),
		Outer:   steppedContour(x-gap, y-gap, x+w+gap, y+h+gap, cout, st.steps, u),
		Caption: caption,
	}
	trim := func(s side, a, b float64) (float64, float64) {
		length, ca, cb := w, cin[0], cin[1]
		switch s {
		case right:
			length, ca, cb = h, cin[1], cin[2]
		case bottom:
			ca, cb = cin[3], cin[2]
		case left:
			length, ca, cb = h, cin[0], cin[3]
		}
		a, b = max(a, (ca+u/3)/length), min(b, 1-(cb+u/3)/length)
		if (b-a)*length < u/2 {
			return 0, 0
		}
		return a, b
	}
	// along returns a strip on the outer line of side s, from..to along it,
	// depth deep, its ends slanted.
	along := func(s side, from, to, depth float64) []Pt {
		length := w
		if s == left || s == right {
			length = h
		}
		depth = min(depth, (to-from)*length/3)
		switch s {
		case top:
			a, b, yy := x+w*from, x+w*to, y-gap
			return []Pt{{a, yy}, {a + depth, yy - depth}, {b - depth, yy - depth}, {b, yy}}
		case bottom:
			a, b, yy := x+w*from, x+w*to, y+h+gap
			return []Pt{{a, yy}, {b, yy}, {b - depth, yy + depth}, {a + depth, yy + depth}}
		case left:
			a, b, xx := y+h*from, y+h*to, x-gap
			return []Pt{{xx, a}, {xx, b}, {xx - depth, b - depth}, {xx - depth, a + depth}}
		default: // right
			a, b, xx := y+h*from, y+h*to, x+w+gap
			return []Pt{{xx, a}, {xx + depth, a + depth}, {xx + depth, b - depth}, {xx, b}}
		}
	}
	for _, pl := range st.plates {
		pl.from, pl.to = trim(pl.side, pl.from, pl.to)
		if pl.to <= pl.from {
			continue
		}
		f.Plates = append(f.Plates, along(pl.side, pl.from, pl.to, thick))
		if !pl.stripes {
			continue
		}
		// Hazard stripes cut through half the plate.
		sw, step := 0.45*u, 1.05*u
		switch pl.side {
		case top:
			a, b, yy := x+w*(pl.from+(pl.to-pl.from)*0.45), x+w*pl.to-thick*1.6, y-gap
			for sx := a; sx+sw < b; sx += step {
				f.Cuts = append(f.Cuts, []Pt{{sx, yy + 0.5}, {sx + thick, yy - thick - 0.5}, {sx + thick + sw, yy - thick - 0.5}, {sx + sw, yy + 0.5}})
			}
		case bottom:
			a, b, yy := x+w*pl.from+thick*1.6, x+w*(pl.from+(pl.to-pl.from)*0.55), y+h+gap
			for sx := a; sx+sw < b; sx += step {
				f.Cuts = append(f.Cuts, []Pt{{sx, yy + thick + 0.5}, {sx + thick, yy - 0.5}, {sx + thick + sw, yy - 0.5}, {sx + sw, yy + thick + 0.5}})
			}
		case left, right:
			a, b := y+h*(pl.from+(pl.to-pl.from)*0.3), y+h*pl.to-thick*1.6
			xx, direction := x-gap, -1.0
			if pl.side == right {
				xx, direction = x+w+gap, 1
			}
			for sy := a; sy+sw < b; sy += step {
				f.Cuts = append(f.Cuts, []Pt{{xx, sy}, {xx + direction*thick, sy + thick}, {xx + direction*thick, sy + thick + sw}, {xx, sy + sw}})
			}
		}
	}
	for _, tr := range st.traces {
		tr.from, tr.to = trim(tr.side, tr.from, tr.to)
		length := w
		if tr.side == left || tr.side == right {
			length = h
		}
		a, b := tr.from*length, tr.to*length
		if b-a < 2*u {
			continue
		}
		var path []Pt
		for _, q := range []Pt{{a, 0.85 * u}, {a + 0.5*u, 1.35 * u}, {b - 0.5*u, 1.35 * u}, {b, 0.85 * u}} {
			switch tr.side {
			case top:
				path = append(path, Pt{x + q[0], y - q[1]})
			case bottom:
				path = append(path, Pt{x + q[0], y + h + q[1]})
			case left:
				path = append(path, Pt{x - q[1], y + q[0]})
			case right:
				path = append(path, Pt{x + w + q[1], y + q[0]})
			}
		}
		f.Traces = append(f.Traces, path)
		f.Nodes = append(f.Nodes, path[0], path[len(path)-1])
	}
	for _, rl := range st.rails {
		rl.from, rl.to = trim(rl.side, rl.from, rl.to)
		if rl.to <= rl.from {
			continue
		}
		// The strip between the inner and the outer line.
		switch rl.side {
		case top:
			a, b := x+w*rl.from, x+w*rl.to
			f.Rails = append(f.Rails, []Pt{{a, y - gap}, {b, y - gap}, {b, y}, {a, y}})
		case bottom:
			a, b := x+w*rl.from, x+w*rl.to
			f.Rails = append(f.Rails, []Pt{{a, y + h}, {b, y + h}, {b, y + h + gap}, {a, y + h + gap}})
		case left:
			a, b := y+h*rl.from, y+h*rl.to
			f.Rails = append(f.Rails, []Pt{{x - gap, a}, {x, a}, {x, b}, {x - gap, b}})
		default:
			a, b := y+h*rl.from, y+h*rl.to
			f.Rails = append(f.Rails, []Pt{{x + w, a}, {x + w + gap, a}, {x + w + gap, b}, {x + w, b}})
		}
	}
	// A solid wedge beyond each big corner cut.
	corners := [4]Pt{{x - gap, y - gap}, {x + w + gap, y - gap}, {x + w + gap, y + h + gap}, {x - gap, y + h + gap}}
	dirs := [4][2]float64{{1, 1}, {-1, 1}, {-1, -1}, {1, -1}}
	for i, c := range st.cuts {
		if c < 2.4 {
			continue
		}
		l := cout[i] * 0.5
		o, d := corners[i], dirs[i]
		f.Plates = append(f.Plates, []Pt{o, {o[0] + d[0]*l, o[1]}, {o[0], o[1] + d[1]*l}})
	}
	for _, t := range st.teeth {
		size := 0.7 * u / w
		if t.side == left || t.side == right {
			size = 0.7 * u / h
		}
		a, b := trim(t.side, t.at, t.at+size)
		if b > a {
			f.Teeth = append(f.Teeth, along(t.side, a, b, 0.3*u))
		}
	}
	// The caption's plate, under the bottom edge; a caption longer than the
	// edge between the corner cuts is cut short.
	if room := int((w-cin[2]-cin[3])/u) - 4; len([]rune(caption)) > room {
		if room < 2 {
			caption = ""
		} else {
			caption = string([]rune(caption)[:room-1]) + "…"
		}
	}
	f.Caption = caption
	if st.label > -2 && caption != "" {
		lw := float64(len([]rune(caption))+2) * u
		lh := float64(gr.CH) * 0.9
		a := x + w*st.label
		if st.label < 0 {
			a = x + (w-lw)/2
		}
		a = math.Max(x+cin[3]+u, math.Min(a, x+w-cin[2]-lw-u))
		yy := y + h + gap
		f.Label = []Pt{{a - 0.5*u, yy}, {a + lw + 0.5*u, yy}, {a + lw + 0.5*u - lh*0.5, yy + lh}, {a - 0.5*u + lh*0.5, yy + lh}}
		f.LabelX = a + u
		f.LabelY = yy + (lh-float64(gr.CH))/2
	}
	if remote { // mirrored, so the caption faces outwards
		m := func(q Pt) Pt { return Pt{2*x + w - q[0], q[1]} }
		mirror := func(ps []Pt) {
			for i := range ps {
				ps[i] = m(ps[i])
			}
			Reverse(ps) // clockwise again
		}
		mirror(f.Inner)
		mirror(f.Outer)
		for _, group := range [][][]Pt{f.Plates, f.Rails, f.Cuts, f.Teeth, f.Traces} {
			for _, ps := range group {
				mirror(ps)
			}
		}
		for i := range f.Nodes {
			f.Nodes[i] = m(f.Nodes[i])
		}
		if f.Label != nil {
			mirror(f.Label)
			f.LabelX = 2*x + w - f.LabelX - float64(len([]rune(caption)))*u
		}
	}
	return f
}

func Reverse(ps []Pt) {
	for i, j := 0, len(ps)-1; i < j; i, j = i+1, j-1 {
		ps[i], ps[j] = ps[j], ps[i]
	}
}

// polygon returns the triangles filling a polygon (to be drawn with the
