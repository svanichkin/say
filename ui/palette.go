package ui

import (
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Colour output: 24-bit escape sequences, or the 16-colour ANSI subset for
// terminals without them, such as Plan 9's vt(1). vt knows SGR 30–37 for the
// foreground (bright with SGR 1, so 16 colours) and 40–47 for the background
// (8 colours, SGR 1 does not brighten it); it ignores 256-colour and RGB
// sequences, or rather reads their numbers as other attributes.

// ColorMode selects how cell colours are written.
type ColorMode int32

const (
	ColorTrue   ColorMode = iota // 24-bit SGR 38;2 / 48;2
	ColorANSI16                  // SGR 30–37 (+1 bright) / 40–47
)

var colorMode atomic.Int32

func init() { colorMode.Store(int32(defaultColorMode())) }

func defaultColorMode() ColorMode {
	if runtime.GOOS == "plan9" {
		return ColorANSI16
	}
	return ColorTrue
}

// SetColorMode applies -colors: "16" (also "ansi", "8"), "truecolor" (also
// "24bit"), or "" / "auto" for the platform default (16 colours on Plan 9).
func SetColorMode(key string) {
	if pixelMode {
		return // the pixel window shows every colour
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "16", "8", "ansi":
		colorMode.Store(int32(ColorANSI16))
	case "truecolor", "24bit", "24":
		colorMode.Store(int32(ColorTrue))
	default:
		colorMode.Store(int32(defaultColorMode()))
	}
	// The window's frames, in characters, where the window's colours show.
	setTermHUD(!ansiColors())
}

func ansiColors() bool { return ColorMode(colorMode.Load()) == ColorANSI16 }

// ansiPalette is vt(1)'s palette (sys/src/cmd/vt/main.c): indices 0–7 are
// SGR 30–37 / 40–47, 8–15 the bright foregrounds (SGR 1;30–37).
var ansiPalette = [16][3]int{
	{0x00, 0x00, 0x00}, {0xAA, 0x00, 0x00}, {0x00, 0xAA, 0x00}, {0xFF, 0x55, 0x00},
	{0x00, 0x00, 0xFF}, {0xAA, 0x00, 0xAA}, {0x00, 0xAA, 0xAA}, {0x7F, 0x7F, 0x7F},
	{0x55, 0x55, 0x55}, {0xFF, 0x55, 0x55}, {0x55, 0xFF, 0x55}, {0xFF, 0xFF, 0x55},
	{0x55, 0x55, 0xFF}, {0xFF, 0x55, 0xFF}, {0x55, 0xFF, 0xFF}, {0xFF, 0xFF, 0xFF},
}

const (
	ansiBgColors = 8  // backgrounds: the first 8
	ansiFgColors = 16 // foregrounds: all
)

// bayer4 is the 4×4 ordered dither matrix (values 0–15).
var bayer4 = [4][4]int{
	{0, 8, 2, 10},
	{12, 4, 14, 6},
	{3, 11, 1, 9},
	{15, 7, 13, 5},
}

// nearestANSI returns the palette index (below n) that shows c in cell
// (row, col): Thomas Knoll's pattern dithering. Each colour has a set of 16
// palette colours whose mean approximates it (skin, say, as grey, orange and
// white in some proportion), ordered by luminance; the Bayer matrix picks
// one per cell. Unlike an offset on brightness alone, this mixes hues.
func nearestANSI(c [3]int, n, row, col int) int {
	t := knollFg
	if n == ansiBgColors {
		t = knollBg
	}
	t.once.Do(t.build)
	key := clamp255(c[0])>>3<<10 | clamp255(c[1])>>3<<5 | clamp255(c[2])>>3
	return int(t.cand[key][bayer4[row&3][col&3]])
}

// knollTable holds the 16 candidates for every colour at 5 bits per
// channel, for one palette size; built once, on first use.
type knollTable struct {
	once sync.Once
	n    int
	cand [1 << 15][16]uint8
}

var (
	knollFg = &knollTable{n: ansiFgColors}
	knollBg = &knollTable{n: ansiBgColors}
)

// knollErrorMul scales the accumulated error fed into the next candidate
// search: higher spreads a colour over more distant palette entries.
const knollErrorMul = 0.35

func (t *knollTable) build() {
	for key := range t.cand {
		c := [3]int{key >> 10 << 3, key >> 5 & 31 << 3, key & 31 << 3}
		for i := range c {
			c[i] |= 4 // the middle of the 8-value bucket
		}
		t.cand[key] = knollCandidates(c, t.n)
	}
}

func knollCandidates(c [3]int, n int) [16]uint8 {
	var out [16]uint8
	var er, eg, eb float64
	for i := range out {
		idx := nearestPlain(
			c[0]+int(er*knollErrorMul),
			c[1]+int(eg*knollErrorMul),
			c[2]+int(eb*knollErrorMul), n)
		out[i] = uint8(idx)
		p := ansiPalette[idx]
		er += float64(c[0] - p[0])
		eg += float64(c[1] - p[1])
		eb += float64(c[2] - p[2])
	}
	sort.SliceStable(out[:], func(a, b int) bool {
		return luma(ansiPalette[out[a]]) < luma(ansiPalette[out[b]])
	})
	return out
}

// nearestPlain is the palette index (below n) closest to (r, g, b).
func nearestPlain(r, g, b, n int) int {
	best, bestDist := 0, math.MaxInt
	for i := 0; i < n; i++ {
		p := ansiPalette[i]
		if d := colorDistance(r, g, b, p[0], p[1], p[2]); d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

func luma(c [3]int) int { return 299*c[0] + 587*c[1] + 114*c[2] }

func clamp255(v int) int { return min(max(v, 0), 255) }

// colorDistance is the "redmean" approximation of perceived distance.
func colorDistance(r1, g1, b1, r2, g2, b2 int) int {
	rm := (r1 + r2) / 2
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return ((512+rm)*dr*dr)>>8 + 4*dg*dg + ((767-rm)*db*db)>>8
}

// solidCell is the glyph for a cell shown entirely in its foreground colour:
// a blank cell's colour drawn this way can use all 16 colours instead of the
// 8 backgrounds.
const solidCell = '█'

// quantizeCell maps a cell's colours to the ANSI palette, in place; row and
// col (in frame cells) select the dither offset. A blank cell becomes a
// solid block when a foreground colour is closer than any background.
func quantizeCell(cell *screenCell, blank bool, row, col int) {
	bg := ansiPalette[nearestANSI(cell.bg, ansiBgColors, row, col)]
	if blank {
		fg := ansiPalette[nearestANSI(cell.bg, ansiFgColors, row, col)]
		if fg != bg {
			cell.r = solidCell
			cell.fg = fg
			cell.bg = bg
			return
		}
		cell.fg, cell.bg = bg, bg
		return
	}
	cell.fg = ansiPalette[nearestANSI(cell.fg, ansiFgColors, row, col)]
	cell.bg = bg
}

// ansiIndex finds a palette colour's index (-1 if it is not one).
func ansiIndex(c [3]int) int {
	for i, p := range ansiPalette {
		if p == c {
			return i
		}
	}
	return -1
}

// appendANSIFg appends the SGR parameters for a palette foreground. The
// intensity is always set, since SGR 1 would otherwise carry over.
func appendANSIFg(b []byte, c [3]int) []byte {
	i := max(ansiIndex(c), 0)
	if i >= 8 {
		b = append(b, "1;3"...)
		i -= 8
	} else {
		b = append(b, "22;3"...)
	}
	return strconv.AppendInt(b, int64(i), 10)
}

// appendANSIBg appends the SGR parameters for a palette background.
func appendANSIBg(b []byte, c [3]int) []byte {
	i := max(ansiIndex(c), 0)
	if i >= 8 {
		i -= 8 // no bright backgrounds; quantizeCell does not produce them
	}
	return strconv.AppendInt(append(b, '4'), int64(i), 10)
}

// panelColors is the SGR sequence for captions and labels: light text on a
// dark bar.
func panelColors() string {
	if ansiColors() {
		return "\x1b[40;1;37m"
	}
	return "\x1b[48;2;20;20;24m\x1b[38;2;245;245;245m"
}
