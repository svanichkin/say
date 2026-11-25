package ui

import (
	"hash/fnv"

	"github.com/svanichkin/say/hud"
)

// Frames in a terminal are drawn as the window draws its own, in lines:
// each frame's style is generated as the window's are (hud.Generate), from
// the same seed (a video pane's, the chat's) or one of its own (a column of
// the home screen): a corner the window cuts deep is rounded, one it cuts
// shallow square; an armour plate is a heavy line (dashed where it is
// hazard-striped), a rail (the gap between the window's two lines filled)
// and a step of the silhouette (out or in) a double one; the rest thin. The screen's border is the window's too:
// its hazard bars dashed heavy lines on the left and the right, its plates
// on the thin top and bottom lines heavy (dashed where striped), its bars'
// cut ends rounded corners.

// lineWeight is how a stretch of a frame's line is drawn.
type lineWeight int

const (
	lineThin lineWeight = iota
	lineHeavy
	lineStriped
	lineDouble
)

var (
	hLines  = [...]rune{'─', '━', '╍', '═'}
	vLines  = [...]rune{'│', '┃', '╏', '║'}
	leftTs  = [...]rune{'├', '┠', '┠', '╟'} // a thin line meeting the left side
	rightTs = [...]rune{'┤', '┨', '┨', '╢'}
)

// roundCut is the depth (cells) from which a corner the window cuts is
// rounded: its big and most of its medium cuts.
const roundCut = 1.5

// frameLines is a frame's style in lines: which corners (top left, top
// right, bottom right, bottom left) are rounded, and the stretches of its
// sides drawn heavy, dashed or double (rails and steps).
type frameLines struct {
	round         [4]bool
	plates, rails []hud.Stretch
	sides         [4]lineWeight // the rest of each side (thin, or the border's bars)
	mirror        bool          // left to right (the peer's pane)
}

// frameLinesOf is the style of the window's frame from seed (chat: the
// chat's, with its small cuts).
func frameLinesOf(seed uint64, chat, mirror bool) frameLines {
	st := hud.StyleOf(seed, chat)
	fl := frameLines{plates: st.Plates(), rails: append(st.Rails(), st.Steps()...), mirror: mirror}
	for i, c := range st.Cuts() {
		fl.round[i] = c >= roundCut
	}
	return fl
}

// borderLines is the screen's border as the window draws its own from seed.
func borderLines(seed uint64) frameLines {
	return frameLines{
		round:  [4]bool{true, true, true, true},
		plates: hud.BorderStretches(seed),
		sides:  [4]lineWeight{lineThin, lineStriped, lineThin, lineStriped},
	}
}

// weight is how the line is drawn at, along side (0 top, 1 right, 2
// bottom, 3 left; 0..1 left to right, top to bottom).
func (fl frameLines) weight(side int, at float64) lineWeight {
	if fl.mirror {
		switch side {
		case 0, 2:
			at = 1 - at
		case 1:
			side = 3
		case 3:
			side = 1
		}
	}
	for _, p := range fl.plates {
		if p.Side == side && at >= p.From && at < p.To {
			if p.Striped {
				return lineStriped
			}
			return lineHeavy
		}
	}
	for _, p := range fl.rails {
		if p.Side == side && at >= p.From && at < p.To {
			return lineDouble
		}
	}
	return fl.sides[side]
}

// corner is corner i's character (top left, top right, bottom right,
// bottom left).
func (fl frameLines) corner(i int) rune {
	j := i
	if fl.mirror {
		j = [4]int{1, 0, 3, 2}[i]
	}
	if fl.round[j] {
		return [4]rune{'╭', '╮', '╯', '╰'}[i]
	}
	return [4]rune{'┌', '┐', '┘', '└'}[i]
}

// hLine is a side's line n cells long (top or bottom).
func (fl frameLines) hLine(side, n int) string {
	rs := make([]rune, n)
	for j := range rs {
		rs[j] = hLines[fl.weight(side, (float64(j)+0.5)/float64(n))]
	}
	return string(rs)
}

// vLine is a side's character on row k of its n (left or right).
func (fl frameLines) vLine(side, k, n int) lineWeight {
	return fl.weight(side, (float64(k)+0.5)/float64(n))
}

// draw puts the frame on the canvas: the rect of cells top, left, cols x
// rows, its outermost cells, in colour c.
func (fl frameLines) draw(h *hudCanvas, top, left, cols, rows int, c [3]int) {
	right, bottom := left+cols-1, top+rows-1
	topLine, bottomLine := []rune(fl.hLine(0, cols-2)), []rune(fl.hLine(2, cols-2))
	for i := range topLine {
		h.set(top, left+1+i, topLine[i], c)
		h.set(bottom, left+1+i, bottomLine[i], c)
	}
	for k := range rows - 2 {
		h.set(top+1+k, left, vLines[fl.vLine(3, k, rows-2)], c)
		h.set(top+1+k, right, vLines[fl.vLine(1, k, rows-2)], c)
	}
	h.set(top, left, fl.corner(0), c)
	h.set(top, right, fl.corner(1), c)
	h.set(bottom, right, fl.corner(2), c)
	h.set(bottom, left, fl.corner(3), c)
}

// homeFrameSeed is the seed of a home screen column's frame: where it is
// in the menu (the keys chosen on the way to it), salted for the launch as
// the window's frames are.
func homeFrameSeed(path []string) uint64 {
	h := fnv.New64a()
	for _, k := range path {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return frameSeed(h.Sum64() ^ 3<<32)
}

// homePath is the keys chosen in the first n columns.
func homePath(n int) []string {
	path := make([]string, 0, n+1)
	for _, col := range home.stack[:min(n, len(home.stack))] {
		if col.cur < len(col.items) {
			path = append(path, col.items[col.cur].key())
		}
	}
	return path
}
