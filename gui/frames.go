//go:build darwin || windows

package gui

import (
	"image/color"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
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

type pt = [2]float64

type paneFrame struct {
	inner   []pt   // the video's edge (and where it shows), clockwise
	outer   []pt   // the line just outside it, clockwise (the ping runs on it)
	plates  [][]pt // armour plates and corner wedges, in the accent colour
	rails   [][]pt // filled stretches between the two lines
	cuts    [][]pt // hazard stripes cut out of the plates (background colour)
	teeth   [][]pt
	label   []pt // the caption's plate (nil: no caption)
	labelX  float64
	labelY  float64 // top of the caption's row
	caption string
	traces  [][]pt // open circuit lines outside the silhouette
	nodes   []pt   // endpoints of those lines
}

// side of a frame: things are laid along it.
type side int

const (
	top side = iota
	right
	bottom
	left
)

// frameStyle describes a frame (see generate).
type frameStyle struct {
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

// generate builds a frame from a seed, by these rules:
//
//   - corners: a big diagonal pair (top left and bottom right, or the other
//     two) with the rest small, or all four medium;
//   - armour plates: 2 to 4 (3 to 4 for the chat), on the top more often than
//     elsewhere, each 18 to 40% of its side, clear of the corners and of one
//     another; the first on the top is cut with hazard stripes, a bottom one
//     sometimes too;
//   - teeth: one or two groups of 2 to 4 on free stretches;
//   - the caption: under the bottom edge on the left, in the middle or on
//     the right (the chat has none); bottom plates keep clear of it.
//
// The same seed gives the same frame throughout a launch, including resizes.
func generate(seed uint64, chat bool) frameStyle {
	r := rand.New(rand.NewPCG(seed, seed^0x5851f42d4c957f2d))
	big := []float64{2.5, 3}[r.IntN(2)]
	small := []float64{0.6, 1.2}[r.IntN(2)]
	var st frameStyle
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
	if !chat {
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
	styleCache = map[styleKey]frameStyle{}
)

type styleKey struct {
	seed uint64
	chat bool
}

// styleOf returns the frame generated from seed (cached).
func styleOf(seed uint64, chat bool) frameStyle {
	key := styleKey{seed, chat}
	styleMu.Lock()
	defer styleMu.Unlock()
	st, ok := styleCache[key]
	if !ok {
		st = generate(seed, chat)
		styleCache[key] = st
	}
	return st
}

// chamfered returns a rectangle with its corners cut by c (top left, top
// right, bottom right, bottom left), clockwise from the top left.
func chamfered(x0, y0, x1, y1 float64, c [4]float64) []pt {
	return []pt{
		{x0, y0 + c[0]}, {x0 + c[0], y0},
		{x1 - c[1], y0}, {x1, y0 + c[1]},
		{x1, y1 - c[2]}, {x1 - c[2], y1},
		{x0 + c[3], y1}, {x0, y1 - c[3]},
	}
}

// steppedContour keeps all edge changes in the gutter. Video and chat
// clipping stays on the simpler inner polygon, clear of decorative notches.
func steppedContour(x0, y0, x1, y1 float64, c [4]float64, steps [4][]edgeStep, u float64) []pt {
	w, h := x1-x0, y1-y0
	edge := func(s side, startCut, endCut float64) []pt {
		length := w
		if s == left || s == right {
			length = h
		}
		var ps []pt
		for _, step := range steps[s] {
			a, b, d := step.from*length, step.to*length, step.depth*u
			shoulder := math.Abs(d)
			if a < startCut+u/2 || b > length-endCut-u/2 || b-a < 2*shoulder+u {
				continue
			}
			for _, p := range []pt{{a, 0}, {a + shoulder, d}, {b - shoulder, d}, {b, 0}} {
				switch s {
				case top:
					ps = append(ps, pt{x0 + p[0], y0 - p[1]})
				case right:
					ps = append(ps, pt{x1 + p[1], y0 + p[0]})
				case bottom:
					ps = append(ps, pt{x0 + p[0], y1 + p[1]})
				case left:
					ps = append(ps, pt{x0 - p[1], y0 + p[0]})
				}
			}
		}
		if s == bottom || s == left {
			reverse(ps)
		}
		return ps
	}
	ps := []pt{{x0, y0 + c[0]}, {x0 + c[0], y0}}
	ps = append(ps, edge(top, c[0], c[1])...)
	ps = append(ps, pt{x1 - c[1], y0}, pt{x1, y0 + c[1]})
	ps = append(ps, edge(right, c[1], c[2])...)
	ps = append(ps, pt{x1, y1 - c[2]}, pt{x1 - c[2], y1})
	ps = append(ps, edge(bottom, c[3], c[2])...)
	ps = append(ps, pt{x0 + c[3], y1}, pt{x0, y1 - c[3]})
	return append(ps, edge(left, c[0], c[3])...)
}

// frameOf lays out pane p's frame in device pixels.
func (g *game) frameOf(p Pane, caption string, st frameStyle) paneFrame {
	u := float64(g.cw)
	x, y := float64(g.ox+p.Col*g.cw), float64(g.oy+p.Row*g.ch)
	w, h := float64(p.Cols*g.cw), float64(p.Rows*g.ch)
	gap, thick := 0.5*u, min(u, w/10, h/6)
	var cin, cout [4]float64
	for i, c := range st.cuts {
		cin[i] = min(c*u, min(w, h)*0.22)
		cout[i] = cin[i] + gap*0.41 // the outer line keeps the cuts' width
	}
	f := paneFrame{
		inner:   chamfered(x, y, x+w, y+h, cin),
		outer:   steppedContour(x-gap, y-gap, x+w+gap, y+h+gap, cout, st.steps, u),
		caption: caption,
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
	along := func(s side, from, to, depth float64) []pt {
		length := w
		if s == left || s == right {
			length = h
		}
		depth = min(depth, (to-from)*length/3)
		switch s {
		case top:
			a, b, yy := x+w*from, x+w*to, y-gap
			return []pt{{a, yy}, {a + depth, yy - depth}, {b - depth, yy - depth}, {b, yy}}
		case bottom:
			a, b, yy := x+w*from, x+w*to, y+h+gap
			return []pt{{a, yy}, {b, yy}, {b - depth, yy + depth}, {a + depth, yy + depth}}
		case left:
			a, b, xx := y+h*from, y+h*to, x-gap
			return []pt{{xx, a}, {xx, b}, {xx - depth, b - depth}, {xx - depth, a + depth}}
		default: // right
			a, b, xx := y+h*from, y+h*to, x+w+gap
			return []pt{{xx, a}, {xx + depth, a + depth}, {xx + depth, b - depth}, {xx, b}}
		}
	}
	for _, pl := range st.plates {
		pl.from, pl.to = trim(pl.side, pl.from, pl.to)
		if pl.to <= pl.from {
			continue
		}
		f.plates = append(f.plates, along(pl.side, pl.from, pl.to, thick))
		if !pl.stripes {
			continue
		}
		// Hazard stripes cut through half the plate.
		sw, step := 0.45*u, 1.05*u
		switch pl.side {
		case top:
			a, b, yy := x+w*(pl.from+(pl.to-pl.from)*0.45), x+w*pl.to-thick*1.6, y-gap
			for sx := a; sx+sw < b; sx += step {
				f.cuts = append(f.cuts, []pt{{sx, yy + 0.5}, {sx + thick, yy - thick - 0.5}, {sx + thick + sw, yy - thick - 0.5}, {sx + sw, yy + 0.5}})
			}
		case bottom:
			a, b, yy := x+w*pl.from+thick*1.6, x+w*(pl.from+(pl.to-pl.from)*0.55), y+h+gap
			for sx := a; sx+sw < b; sx += step {
				f.cuts = append(f.cuts, []pt{{sx, yy + thick + 0.5}, {sx + thick, yy - 0.5}, {sx + thick + sw, yy - 0.5}, {sx + sw, yy + thick + 0.5}})
			}
		case left, right:
			a, b := y+h*(pl.from+(pl.to-pl.from)*0.3), y+h*pl.to-thick*1.6
			xx, direction := x-gap, -1.0
			if pl.side == right {
				xx, direction = x+w+gap, 1
			}
			for sy := a; sy+sw < b; sy += step {
				f.cuts = append(f.cuts, []pt{{xx, sy}, {xx + direction*thick, sy + thick}, {xx + direction*thick, sy + thick + sw}, {xx, sy + sw}})
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
		var path []pt
		for _, q := range []pt{{a, 0.85 * u}, {a + 0.5*u, 1.35 * u}, {b - 0.5*u, 1.35 * u}, {b, 0.85 * u}} {
			switch tr.side {
			case top:
				path = append(path, pt{x + q[0], y - q[1]})
			case bottom:
				path = append(path, pt{x + q[0], y + h + q[1]})
			case left:
				path = append(path, pt{x - q[1], y + q[0]})
			case right:
				path = append(path, pt{x + w + q[1], y + q[0]})
			}
		}
		f.traces = append(f.traces, path)
		f.nodes = append(f.nodes, path[0], path[len(path)-1])
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
			f.rails = append(f.rails, []pt{{a, y - gap}, {b, y - gap}, {b, y}, {a, y}})
		case bottom:
			a, b := x+w*rl.from, x+w*rl.to
			f.rails = append(f.rails, []pt{{a, y + h}, {b, y + h}, {b, y + h + gap}, {a, y + h + gap}})
		case left:
			a, b := y+h*rl.from, y+h*rl.to
			f.rails = append(f.rails, []pt{{x - gap, a}, {x, a}, {x, b}, {x - gap, b}})
		default:
			a, b := y+h*rl.from, y+h*rl.to
			f.rails = append(f.rails, []pt{{x + w, a}, {x + w + gap, a}, {x + w + gap, b}, {x + w, b}})
		}
	}
	// A solid wedge beyond each big corner cut.
	corners := [4]pt{{x - gap, y - gap}, {x + w + gap, y - gap}, {x + w + gap, y + h + gap}, {x - gap, y + h + gap}}
	dirs := [4][2]float64{{1, 1}, {-1, 1}, {-1, -1}, {1, -1}}
	for i, c := range st.cuts {
		if c < 2.4 {
			continue
		}
		l := cout[i] * 0.5
		o, d := corners[i], dirs[i]
		f.plates = append(f.plates, []pt{o, {o[0] + d[0]*l, o[1]}, {o[0], o[1] + d[1]*l}})
	}
	for _, t := range st.teeth {
		size := 0.7 * u / w
		if t.side == left || t.side == right {
			size = 0.7 * u / h
		}
		a, b := trim(t.side, t.at, t.at+size)
		if b > a {
			f.teeth = append(f.teeth, along(t.side, a, b, 0.3*u))
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
	f.caption = caption
	if st.label > -2 && caption != "" {
		lw := float64(len([]rune(caption))+2) * u
		lh := float64(g.ch) * 0.9
		a := x + w*st.label
		if st.label < 0 {
			a = x + (w-lw)/2
		}
		a = math.Max(x+cin[3]+u, math.Min(a, x+w-cin[2]-lw-u))
		yy := y + h + gap
		f.label = []pt{{a - 0.5*u, yy}, {a + lw + 0.5*u, yy}, {a + lw + 0.5*u - lh*0.5, yy + lh}, {a - 0.5*u + lh*0.5, yy + lh}}
		f.labelX = a + u
		f.labelY = yy + (lh-float64(g.ch))/2
	}
	if p.Remote { // mirrored, so the caption faces outwards
		m := func(q pt) pt { return pt{2*x + w - q[0], q[1]} }
		mirror := func(ps []pt) {
			for i := range ps {
				ps[i] = m(ps[i])
			}
			reverse(ps) // clockwise again
		}
		mirror(f.inner)
		mirror(f.outer)
		for _, group := range [][][]pt{f.plates, f.rails, f.cuts, f.teeth, f.traces} {
			for _, ps := range group {
				mirror(ps)
			}
		}
		for i := range f.nodes {
			f.nodes[i] = m(f.nodes[i])
		}
		if f.label != nil {
			mirror(f.label)
			f.labelX = 2*x + w - f.labelX - float64(len([]rune(caption)))*u
		}
	}
	return f
}

func reverse(ps []pt) {
	for i, j := 0, len(ps)-1; i < j; i, j = i+1, j-1 {
		ps[i], ps[j] = ps[j], ps[i]
	}
}

// polygon returns the triangles filling a polygon (to be drawn with the
// non-zero rule: it may be concave).
func polygon(poly []pt) ([]ebiten.Vertex, []uint16) {
	var path vector.Path
	path.MoveTo(float32(poly[0][0]), float32(poly[0][1]))
	for _, q := range poly[1:] {
		path.LineTo(float32(q[0]), float32(q[1]))
	}
	path.Close()
	return path.AppendVerticesAndIndicesForFilling(nil, nil)
}

// paneSeed and paneColour select a pane's geometry and accent.
func (g *game) paneSeed(p Pane) uint64 {
	self, peer, chat := uint64(1), uint64(2), uint64(3)
	if g.in.Seeds != nil {
		self, peer, chat = g.in.Seeds()
	}
	switch {
	case p.Chat:
		return chat
	case p.Remote:
		return peer
	}
	return self
}

func (g *game) paneColour(p Pane) color.RGBA {
	if p.Chat {
		return g.r.chatAccent
	}
	if p.Remote {
		return g.r.accent[1]
	}
	return g.r.accent[0]
}

type frameLayoutKey struct {
	row, col, cols, rows int
	cw, ch, ox, oy       int
	caption              string
	seed                 uint64
	remote, chat         bool
}

type cachedPaneFrame struct {
	key   frameLayoutKey
	frame paneFrame
}

func (g *game) paneFrame(p Pane) paneFrame {
	key := g.paneFrameKey(p)
	index := 0
	if p.Remote {
		index = 1
	}
	if p.Chat {
		index = 2
	}
	cached := &g.r.frames[index]
	if cached.frame.inner == nil || cached.key != key {
		cached.key = key
		cached.frame = g.frameOf(p, p.Caption, styleOf(key.seed, p.Chat))
	}
	return cached.frame
}

// drawPanes draws each pane's video, a pixel per subpixel of its cells,
// fitted into the pane and cut to the frame's inner line.
func (g *game) drawPanes(dst *ebiten.Image, panes []Pane) {
	if g.r.paneImgs == nil {
		g.r.paneImgs = map[[2]int]*paneImage{}
	}
	for _, p := range panes {
		if p.Chat {
			continue
		}
		f := g.frameImage(p, g.paneColour(p))
		if f == nil {
			continue
		}
		fr := g.paneFrame(p)
		px, py := float64(g.ox+p.Col*g.cw), float64(g.oy+p.Row*g.ch)
		pw, ph := float64(p.Cols*g.cw), float64(p.Rows*g.ch)
		iw, ih := float64(f.Bounds().Dx()), float64(f.Bounds().Dy())
		k := math.Min(pw/iw, ph/ih)
		fx, fy := px+(pw-iw*k)/2, py+(ph-ih*k)/2
		vs, is := polygon(fr.inner)
		for i := range vs {
			vs[i].SrcX = float32((float64(vs[i].DstX) - fx) / k)
			vs[i].SrcY = float32((float64(vs[i].DstY) - fy) / k)
			vs[i].ColorR, vs[i].ColorG, vs[i].ColorB, vs[i].ColorA = 1, 1, 1, 1
		}
		dst.DrawTriangles(vs, is, f, &ebiten.DrawTrianglesOptions{Filter: ebiten.FilterNearest, FillRule: ebiten.FillRuleNonZero})
	}
}

// drawFrames draws the frames over the cells and the text. They change only
// with the layout, a caption's length or a colour, so they are drawn once into a
// layer of their own (and one for their glow) and the layers are laid on
// every frame: drawing them shape by shape, antialiased, takes tens of
// milliseconds and made resizing the window stall.
func (g *game) drawFrames(dst *ebiten.Image, panes []Pane) {
	r := &g.r
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	gw, gh := r.glow.Bounds().Dx(), r.glow.Bounds().Dy()
	key := framesKey{w: w, h: h}
	for _, p := range panes {
		// The caption's plate follows only its length: the text, which
		// changes every second, is written over it (drawCaptions).
		layout := g.paneFrameKey(p)
		layout.caption = strconv.Itoa(utf8.RuneCountInString(p.Caption))
		key.panes = append(key.panes, paneKey{layout, g.paneColour(p)})
	}
	if r.frameLayer == nil || !key.equal(r.framesDrawn) {
		r.frameLayer = grown(r.frameLayer, w, h)
		r.frameGlow = grown(r.frameGlow, gw, gh)
		r.frameLayer.Clear()
		r.frameGlow.Clear()
		g.drawBorder(w, h)
		for _, p := range panes {
			g.frameShapes(p)
		}
		r.framesDrawn = key
	}
	dst.DrawImage(r.frameLayer, nil)
	r.glow.DrawImage(r.frameGlow, nil)
}

// frameShapes draws a pane's frame into the frame layers: its shapes go in
// a few batches, one draw per colour.
func (g *game) frameShapes(p Pane) {
	r := &g.r
	fr := g.paneFrame(p)
	c := g.paneColour(p)
	u := float64(g.cw)
	dimmer := color.RGBA{c.R / 3 * 2, c.G / 3 * 2, c.B / 3 * 2, 255}
	var dim, bright, cuts, label, dimGlow, glow shapeBatch
	for _, ps := range fr.rails {
		dim.add(ps, 1)
	}
	inner, outer, trace := max(1, u/10), max(1.5, u/7), max(1, u/12)
	dim.loop(fr.inner, inner, 1)
	dimGlow.loop(fr.inner, inner*2, glowScale)
	bright.loop(fr.outer, outer, 1)
	glow.loop(fr.outer, outer*2, glowScale)
	for _, path := range fr.traces {
		for i := 1; i < len(path); i++ {
			bright.line(path[i-1], path[i], trace, 1)
			glow.line(path[i-1], path[i], trace*2, glowScale)
		}
	}
	shapes := append([][]pt{}, fr.plates...)
	shapes = append(shapes, fr.teeth...)
	for _, node := range fr.nodes {
		d := u * 0.16
		shapes = append(shapes, []pt{{node[0] - d, node[1]}, {node[0], node[1] - d}, {node[0] + d, node[1]}, {node[0], node[1] + d}})
	}
	for _, ps := range shapes {
		bright.add(ps, 1)
		glow.add(ps, glowScale)
	}
	for _, ps := range fr.cuts {
		cuts.add(ps, 1)
	}
	if fr.label != nil { // a dark plate with a bright edge: the caption glows on it
		label.add(fr.label, 1)
		bright.loop(fr.label, outer, 1)
		glow.loop(fr.label, outer*2, glowScale)
	}
	dim.draw(r.frameLayer, dimmer, r.white)
	label.draw(r.frameLayer, color.RGBA{c.R / 5, c.G / 5, c.B / 5, 255}, r.white)
	bright.draw(r.frameLayer, c, r.white)
	cuts.draw(r.frameLayer, defaultBg, r.white)
	dimGlow.draw(r.frameGlow, dimmer, r.white)
	glow.draw(r.frameGlow, c, r.white)
}

// whiter mixes c with white, k of the way.
func whiter(c color.RGBA, k float64) color.RGBA {
	mix := func(v uint8) uint8 { return uint8(float64(v) + (255-float64(v))*k) }
	return color.RGBA{mix(c.R), mix(c.G), mix(c.B), 255}
}

// framesKey is what the frame layers were drawn for.
type framesKey struct {
	w, h  int
	panes []paneKey
}

type paneKey struct {
	layout frameLayoutKey
	colour color.RGBA
}

func (k framesKey) equal(o framesKey) bool {
	return k.w == o.w && k.h == o.h && slices.Equal(k.panes, o.panes)
}

// grown returns img when it holds w x h, or a new, larger one: layers are
// drawn from the corner, so a larger one does, and resizing the window does
// not allocate on every step.
func grown(img *ebiten.Image, w, h int) *ebiten.Image {
	if img != nil && img.Bounds().Dx() >= w && img.Bounds().Dy() >= h {
		return img
	}
	if img != nil {
		img.Deallocate()
	}
	const step = 256
	return ebiten.NewImage((w+step-1)/step*step, (h+step-1)/step*step)
}

// shapeBatch gathers filled shapes of one colour into one draw. Every shape
// is turned the same way round, so where they overlap they add up (the
// non-zero rule) instead of cancelling.
type shapeBatch struct {
	vs []ebiten.Vertex
	is []uint16
}

func (b *shapeBatch) add(poly []pt, scale float64) {
	if len(poly) < 3 {
		return
	}
	area := 0.0
	for i := range poly {
		a, c := poly[i], poly[(i+1)%len(poly)]
		area += a[0]*c[1] - c[0]*a[1]
	}
	var path vector.Path
	at := func(i int) pt {
		if area < 0 {
			return poly[len(poly)-1-i]
		}
		return poly[i]
	}
	q := at(0)
	path.MoveTo(float32(q[0]*scale), float32(q[1]*scale))
	for i := 1; i < len(poly); i++ {
		q = at(i)
		path.LineTo(float32(q[0]*scale), float32(q[1]*scale))
	}
	path.Close()
	b.vs, b.is = path.AppendVerticesAndIndicesForFilling(b.vs, b.is)
}

// line adds a straight stroke, width wide.
func (b *shapeBatch) line(a, c pt, width, scale float64) {
	dx, dy := c[0]-a[0], c[1]-a[1]
	n := math.Hypot(dx, dy)
	if n == 0 {
		return
	}
	nx, ny := -dy/n*width/2, dx/n*width/2
	b.add([]pt{{a[0] + nx, a[1] + ny}, {c[0] + nx, c[1] + ny}, {c[0] - nx, c[1] - ny}, {a[0] - nx, a[1] - ny}}, scale)
}

// loop adds a closed polyline.
func (b *shapeBatch) loop(ps []pt, width, scale float64) {
	for i := range ps {
		b.line(ps[i], ps[(i+1)%len(ps)], width, scale)
	}
}

// tinted adds a convex shape in a colour of its own (c not premultiplied).
// A batch of tinted shapes is drawn with drawTinted: the shapes overlap and
// blend, later over earlier.
func (b *shapeBatch) tinted(poly []pt, scale float64, c color.RGBA) {
	if len(poly) < 3 {
		return
	}
	base := uint16(len(b.vs))
	a := float32(c.A) / 255
	for _, q := range poly {
		b.vs = append(b.vs, ebiten.Vertex{
			DstX: float32(q[0] * scale), DstY: float32(q[1] * scale),
			ColorR: float32(c.R) / 255 * a, ColorG: float32(c.G) / 255 * a, ColorB: float32(c.B) / 255 * a, ColorA: a,
		})
	}
	for i := 1; i+1 < len(poly); i++ { // a fan: the shape is convex
		b.is = append(b.is, base, base+uint16(i), base+uint16(i+1))
	}
}

func (b *shapeBatch) drawTinted(img *ebiten.Image, white *ebiten.Image) {
	if len(b.is) == 0 {
		return
	}
	img.DrawTriangles(b.vs, b.is, white, &ebiten.DrawTrianglesOptions{AntiAlias: true})
}

func (b *shapeBatch) draw(img *ebiten.Image, c color.RGBA, white *ebiten.Image) {
	if len(b.is) == 0 {
		return
	}
	a := float32(c.A) / 255
	for i := range b.vs {
		b.vs[i].SrcX, b.vs[i].SrcY = 0, 0
		b.vs[i].ColorR, b.vs[i].ColorG, b.vs[i].ColorB, b.vs[i].ColorA = float32(c.R)/255*a, float32(c.G)/255*a, float32(c.B)/255*a, a
	}
	img.DrawTriangles(b.vs, b.is, white, &ebiten.DrawTrianglesOptions{AntiAlias: true, FillRule: ebiten.FillRuleNonZero})
}

// paneFrameKey is what a pane's frame geometry follows from.
func (g *game) paneFrameKey(p Pane) frameLayoutKey {
	return frameLayoutKey{p.Row, p.Col, p.Cols, p.Rows, g.cw, g.ch, g.ox, g.oy, p.Caption, g.paneSeed(p), p.Remote, p.Chat}
}

// drawCaptions writes the captions on their plates: with glow, in their
// frames' colours into the glow layer only (before it is laid on); then,
// crisp and nearly white, over it.
func (g *game) drawCaptions(dst *ebiten.Image, panes []Pane, glow bool) {
	for _, p := range panes {
		if fr := g.paneFrame(p); fr.label != nil {
			c := g.paneColour(p)
			if !glow {
				c = whiter(c, 0.75) // a neon sign: white letters, the glow in colour
			}
			g.drawCaption(dst, fr, c, glow)
		}
	}
}

// frameImage returns the pane's frame as an image (a pixel per subpixel),
// rasterizing it when it is a new one; a black and white frame is tinted
// with tint, its owner's colour.
func (g *game) frameImage(p Pane, tint color.RGBA) *ebiten.Image {
	if g.in.Frame == nil || g.in.FrameRGB == nil {
		return nil
	}
	f := g.in.Frame(p.Frame)
	if f == nil || f.Cols <= 0 || f.Rows <= 0 {
		return nil
	}
	r := &g.r
	key := [2]int{p.Row, p.Col}
	pi := r.paneImgs[key]
	w, h := f.Cols*cellW, f.Rows*cellH
	if pi == nil || pi.img.Bounds().Dx() != w || pi.img.Bounds().Dy() != h {
		if pi != nil {
			pi.img.Deallocate()
		}
		pi = &paneImage{img: ebiten.NewImage(w, h), pix: make([]byte, w*h*4)}
		r.paneImgs[key] = pi
	}
	if pi.frame != p.Frame {
		g.rasterize(f, pi.pix, w, tint)
		pi.img.WritePixels(pi.pix)
		pi.frame = p.Frame
	}
	return pi.img
}

// drawCaption writes the caption on its plate, digits bold (with glow: into
// the glow layer instead).
func (g *game) drawCaption(dst *ebiten.Image, fr paneFrame, c color.RGBA, glow bool) {
	m := g.font.Metrics()
	top := fr.labelY + (float64(g.ch)-(m.HAscent+m.HDescent))/2
	x := fr.labelX
	for _, r := range fr.caption {
		s := string(r)
		bold := 0.0
		if r >= '0' && r <= '9' {
			bold = g.font.BoldShift()
		}
		op := &text.DrawOptions{}
		op.GeoM.Translate(x, top)
		op.ColorScale.ScaleWithColor(c)
		if glow {
			g.glowText(s, op, bold, 0.5)
		} else {
			g.drawChar(dst, s, op, bold)
		}
		x += float64(g.cw)
	}
}
