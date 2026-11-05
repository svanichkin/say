package codec

import (
	"math"
	"math/rand"
	"testing"
)

// Each cell contributes two colors; the palette must never exceed what a
// one-byte index can address, even when it is one short of full.
func TestBuildPaletteNeverExceedsByteIndex(t *testing.T) {
	const cells = 400
	fg := make([]paletteColor, cells)
	bg := make([]paletteColor, cells)
	for i := 0; i < cells; i++ {
		// Distinct after quantization to 6-bit Y / 5-bit Cb.
		a, b := 2*i, 2*i+1
		fg[i] = paletteColor{byte(a%64) << 2, byte(a/64) << 3, 0}
		bg[i] = paletteColor{byte(b%64) << 2, byte(b/64) << 3, 0}
	}
	// Make cell 127 add only one new color so the count reaches 255 before cell 128.
	bg[127] = fg[0]
	palette := buildPaletteFromColors(fg, bg, cells)
	if len(palette) > maxPaletteColors {
		t.Fatalf("palette has %d entries, want at most %d", len(palette), maxPaletteColors)
	}
	if len(palette) != maxPaletteColors {
		t.Fatalf("palette has %d entries, want it filled to %d", len(palette), maxPaletteColors)
	}
}

// The matcher must pick exactly what a full scan picks: the smallest
// distance, and the lowest index among equal distances.
func TestPaletteMatcherMatchesFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for round := 0; round < 200; round++ {
		colors := make([]paletteColor, 1+rng.Intn(600))
		for i := range colors {
			if rng.Intn(3) == 0 && i > 0 {
				colors[i] = colors[rng.Intn(i)] // repeats hit the table
				continue
			}
			colors[i] = paletteColor{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))}
		}
		palette := buildPaletteFromColors(colors[:len(colors)/2], colors[len(colors)/2:], len(colors)/2)
		if round%7 == 0 && len(palette) > 1 { // unsorted palettes fall back to a full scan
			rng.Shuffle(len(palette), func(i, j int) { palette[i], palette[j] = palette[j], palette[i] })
		}
		expanded := make([][3]int, len(palette))
		for i, p := range palette {
			expanded[i] = expandPaletteEntry(p)
		}
		var m paletteMatcher
		m.reset(expanded)
		for pass := 0; pass < 3; pass++ { // the answers carry across passes
			m.reserve(len(colors))
			for _, c := range colors {
				want, wantDist := 0, math.MaxInt
				for j := range expanded {
					if d := colorDistance(expanded[j], c); d < wantDist {
						want, wantDist = j, d
					}
				}
				if len(palette) == 0 {
					continue
				}
				got, dist := m.index(c)
				if int(got) != want || dist != wantDist {
					t.Fatalf("round %d colour %v: got %d (%d), want %d (%d)", round, c, got, dist, want, wantDist)
				}
			}
		}
	}
}
