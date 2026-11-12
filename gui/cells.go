package gui

import (
	"sync"

	"github.com/svanichkin/say/codec"
)

// Characters drawn on the 4x8 cell grid rather than from the font: the
// video's glyphs (their exact codec shapes, so the picture looks as it was
// encoded) and the rest of box drawing and block elements, which the
// interface uses for borders and bars.

const (
	cellW = 4
	cellH = 8
)

// A pattern is a 4x8 cell: bit 31 is the top left pixel, rows of 4 follow.
func row(r int, bits uint32) uint32 { return (bits & 0xf) << uint(28-4*r) }

func rows(from, to int, bits uint32) uint32 {
	var p uint32
	for r := from; r < to; r++ {
		p |= row(r, bits)
	}
	return p
}

var (
	cellOnce     sync.Once
	cellPatterns map[rune]uint32
)

func buildCells() {
	m := map[rune]uint32{}
	// Block elements.
	m['▀'] = rows(0, 4, 0xf)
	for n := 1; n <= 8; n++ { // ▁▂▃▄▅▆▇█: the lower n eighths
		m[rune(0x2580+n)] = rows(8-n, 8, 0xf)
	}
	for n := 7; n >= 1; n-- { // ▉▊▋▌▍▎▏: the left n eighths, in 4 px
		w := max(1, (n*cellW+4)/8)
		m[rune(0x2590-n)] = rows(0, 8, (0xf<<uint(4-w))&0xf)
	}
	m['▐'] = rows(0, 8, 0x3)
	m['░'] = row(0, 0x8) | row(1, 0x2) | row(2, 0x8) | row(3, 0x2) | row(4, 0x8) | row(5, 0x2) | row(6, 0x8) | row(7, 0x2)
	m['▒'] = row(0, 0xa) | row(1, 0x5) | row(2, 0xa) | row(3, 0x5) | row(4, 0xa) | row(5, 0x5) | row(6, 0xa) | row(7, 0x5)
	m['▓'] = ^m['░']
	m['▔'] = row(0, 0xf)
	m['▕'] = rows(0, 8, 0x1)
	// Quadrants: upper left, upper right, lower left, lower right.
	ul, ur, ll, lr := rows(0, 4, 0xc), rows(0, 4, 0x3), rows(4, 8, 0xc), rows(4, 8, 0x3)
	quads := map[rune]uint32{
		'▖': ll, '▗': lr, '▘': ul, '▙': ul | ll | lr, '▚': ul | lr,
		'▛': ul | ur | ll, '▜': ul | ur | lr, '▝': ur, '▞': ur | ll, '▟': ur | ll | lr,
	}
	for r, p := range quads {
		m[r] = p
	}
	// Box drawing: arms from the centre (up, down, left, right), light or
	// heavy, on the lines the codec's own ─ │ ━ ┃ use.
	light := boxLines('─', '│')
	heavy := boxLines('━', '┃')
	for r, arms := range boxArms {
		var p uint32
		for i, a := range arms {
			switch a {
			case 'l':
				p |= light[i]
			case 'h':
				p |= heavy[i]
			}
		}
		m[r] = p
	}
	// The codec's glyphs last: their shapes win.
	for _, cp := range codec.GlyphCodepoints() {
		if p, ok := codec.GlyphPattern(cp); ok {
			m[rune(cp)] = p
		}
	}
	cellPatterns = m
}

// boxLines splits a horizontal and a vertical line into their four arms.
func boxLines(h, v rune) [4]uint32 {
	hp, _ := codec.GlyphPattern(int(h))
	vp, _ := codec.GlyphPattern(int(v))
	// The vertical line's columns and the horizontal line's rows.
	var vcols, hrows uint32
	for r := 0; r < 8; r++ {
		vcols |= vp >> uint(28-4*r) & 0xf
		if hp>>uint(28-4*r)&0xf != 0 {
			hrows |= 1 << uint(r)
		}
	}
	firstRow, lastRow := 8, -1
	for r := 0; r < 8; r++ {
		if hrows&(1<<uint(r)) != 0 {
			firstRow, lastRow = min(firstRow, r), max(lastRow, r)
		}
	}
	firstCol, lastCol := 4, -1
	for c := 0; c < 4; c++ {
		if vcols&(0x8>>uint(c)) != 0 {
			firstCol, lastCol = min(firstCol, c), max(lastCol, c)
		}
	}
	if lastRow < 0 || lastCol < 0 {
		return [4]uint32{}
	}
	leftMask := uint32(0xf) << uint(4-lastCol-1) & 0xf // columns 0..lastCol
	rightMask := uint32(0xf) >> uint(firstCol)         // columns firstCol..3
	var left, right uint32
	for r := firstRow; r <= lastRow; r++ {
		left |= row(r, leftMask)
		right |= row(r, rightMask)
	}
	up := rows(0, lastRow+1, vcols)
	down := rows(firstRow, 8, vcols)
	return [4]uint32{up, down, left, right}
}

// boxArms: up, down, left, right; '.' none, 'l' light, 'h' heavy.
var boxArms = map[rune]string{
	'─': "..ll", '━': "..hh", '│': "ll..", '┃': "hh..",
	'┌': ".l.l", '┐': ".ll.", '└': "l..l", '┘': "l.l.",
	'┏': ".h.h", '┓': ".hh.", '┗': "h..h", '┛': "h.h.",
	'├': "ll.l", '┤': "lll.", '┬': ".lll", '┴': "l.ll", '┼': "llll",
	'┣': "hh.h", '┫': "hhh.", '┳': ".hhh", '┻': "h.hh", '╋': "hhhh",
	'╴': "..l.", '╵': "l...", '╶': "...l", '╷': ".l..",
	'╸': "..h.", '╹': "h...", '╺': "...h", '╻': ".h..",
	'╭': ".l.l", '╮': ".ll.", '╯': "l.l.", '╰': "l..l",
}

// cellPattern returns the cell shape of r if r is drawn on the cell grid.
func cellPattern(r rune) (uint32, bool) {
	cellOnce.Do(buildCells)
	p, ok := cellPatterns[r]
	return p, ok
}
