package ui

import (
	"strings"
	"testing"
)

func TestNearestANSIExactColours(t *testing.T) {
	// A palette colour keeps itself in most of the 4×4 dither cells (the
	// two greys, 0x55 and 0x7F, are closer than the dither swing, so they
	// mix a little).
	for i, p := range ansiPalette {
		same := 0
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				if ansiPalette[nearestANSI(p, ansiFgColors, row, col)] == p {
					same++
				}
			}
		}
		if same < 10 {
			t.Errorf("colour %d %v keeps itself in %d of 16 cells", i, p, same)
		}
	}
}

func TestNearestANSIDithersBetweenLevels(t *testing.T) {
	// Halfway between black and grey 0x55, the 4×4 cells mix both.
	seen := map[int]int{}
	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			seen[nearestANSI([3]int{0x2a, 0x2a, 0x2a}, ansiFgColors, row, col)]++
		}
	}
	if seen[0] == 0 || seen[8] == 0 || seen[0]+seen[8] != 16 {
		t.Fatalf("mid grey dithers to %v, want black (0) and grey (8)", seen)
	}
}

func TestQuantizeCellBackgroundOnly8(t *testing.T) {
	cell := screenCell{r: '▄', fg: [3]int{255, 255, 255}, bg: [3]int{255, 255, 255}}
	quantizeCell(&cell, false, 0, 0)
	if i := ansiIndex(cell.bg); i < 0 || i >= ansiBgColors {
		t.Fatalf("background %v is not one of the 8 background colours", cell.bg)
	}
	if cell.fg != ansiPalette[15] {
		t.Fatalf("foreground %v, want bright white", cell.fg)
	}
}

func TestQuantizeCellBlankUsesSolidBlock(t *testing.T) {
	// White exists only as a foreground: a blank white cell becomes a block.
	cell := screenCell{r: ' ', bg: [3]int{255, 255, 255}}
	quantizeCell(&cell, true, 0, 0)
	if cell.r != solidCell || cell.fg != ansiPalette[15] {
		t.Fatalf("blank white cell -> %q fg %v, want solid bright white", cell.r, cell.fg)
	}
	// Black is a background: the cell stays blank.
	cell = screenCell{r: ' ', bg: [3]int{0, 0, 0}}
	quantizeCell(&cell, true, 0, 0)
	if cell.r != ' ' || cell.bg != ansiPalette[0] {
		t.Fatalf("blank black cell -> %q bg %v, want blank black", cell.r, cell.bg)
	}
}

func TestWriteCellColorsANSI(t *testing.T) {
	SetColorMode("16")
	defer SetColorMode("")
	var sb strings.Builder
	writeCellColors(&sb, screenCell{fg: ansiPalette[9], bg: ansiPalette[4]}, true, true)
	writeCellColors(&sb, screenCell{fg: ansiPalette[1]}, true, false)
	if got, want := sb.String(), "\x1b[1;31;44m\x1b[22;31m"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(panelColors(), "38;2") {
		t.Fatalf("panel colours %q use 24-bit colour in 16-colour mode", panelColors())
	}
}

func TestSetColorMode(t *testing.T) {
	defer SetColorMode("")
	for in, want := range map[string]ColorMode{"16": ColorANSI16, "ansi": ColorANSI16, "truecolor": ColorTrue, "24bit": ColorTrue} {
		SetColorMode(in)
		if got := ColorMode(colorMode.Load()); got != want {
			t.Errorf("SetColorMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestKnollMixesHues(t *testing.T) {
	// Colours between the palette's (skin tones among them) come out right
	// on average over the 4×4 pattern, as a mix of several hues.
	for _, c := range [][3]int{{200, 150, 120}, {230, 190, 160}, {150, 100, 80}, {60, 120, 200}} {
		var sum [3]int
		used := map[int]bool{}
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				i := nearestANSI(c, ansiFgColors, row, col)
				used[i] = true
				for k := range sum {
					sum[k] += ansiPalette[i][k]
				}
			}
		}
		for k := range sum {
			if d := sum[k]/16 - c[k]; d < -16 || d > 16 {
				t.Errorf("%v: mean %v, channel %d off by %d", c, [3]int{sum[0] / 16, sum[1] / 16, sum[2] / 16}, k, d)
			}
		}
		if len(used) < 2 {
			t.Errorf("%v: one palette colour only", c)
		}
	}
}
