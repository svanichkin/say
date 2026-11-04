package codec

import "testing"

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
	palette, expanded := buildPaletteFromColors(fg, bg, cells)
	if len(palette) > maxPaletteColors || len(expanded) != len(palette) {
		t.Fatalf("palette has %d entries, want at most %d", len(palette), maxPaletteColors)
	}
	if len(palette) != maxPaletteColors {
		t.Fatalf("palette has %d entries, want it filled to %d", len(palette), maxPaletteColors)
	}
}
