package codec

import (
	"math/rand"
	"testing"
)

func glyphChurn(t *testing.T, hold int) (perFrame float64, cells int) {
	t.Helper()
	const w, h, cols, rows = 352, 288, 88, 36
	rng := rand.New(rand.NewSource(3))
	base := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 3
			base[o] = byte(40 + (x*3+y*2)%180) // smooth gradients and edges
			base[o+1] = byte(110 + x%30)
			base[o+2] = byte(120 + y%25)
		}
	}
	enc := NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(hold)
	dec := NewDecoder()
	var prev []byte
	changes, frames := 0, 0
	for f := 0; f < 40; f++ {
		data := make([]byte, len(base))
		for i := range data {
			data[i] = byte(min(255, max(0, int(base[i])+rng.Intn(13)-6))) // camera noise
		}
		out, err := enc.Encode(Frame{Data: data, Width: w, Height: h}, cols, rows)
		if err != nil {
			t.Fatal(err)
		}
		tf, err := dec.Decode(out)
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil && f > 5 {
			for i := range tf.Glyphs {
				if tf.Glyphs[i] != prev[i] {
					changes++
				}
			}
			frames++
		}
		prev = append(prev[:0], tf.Glyphs...)
	}
	return float64(changes) / float64(frames), cols * rows
}

func TestShapeHoldStopsGlyphShimmer(t *testing.T) {
	off, cells := glyphChurn(t, 0)
	on, _ := glyphChurn(t, 10)
	t.Logf("glyphs changing per frame on a still noisy picture (%d cells): %.0f without hold, %.0f with hold 10", cells, off, on)
	if on > off/10 {
		t.Fatalf("hold did not reduce shimmer enough: %.0f -> %.0f", off, on)
	}
}

func TestShapeHoldLetsRealChangesThrough(t *testing.T) {
	const w, h = 64, 64
	dark := make([]byte, w*h*3)
	light := make([]byte, w*h*3)
	for i := range light {
		light[i] = 230
	}
	for y := 0; y < h; y++ { // left half light: an edge inside the cells
		for x := 0; x < w/2; x++ {
			o := (y*w + x) * 3
			dark[o], dark[o+1], dark[o+2] = 230, 128, 128
		}
	}
	enc := NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(10)
	dec := NewDecoder()
	if _, err := enc.Encode(Frame{Data: dark, Width: w, Height: h}, 16, 8); err != nil {
		t.Fatal(err)
	}
	a, _ := enc.Encode(Frame{Data: light, Width: w, Height: h}, 16, 8)
	tf, err := dec.Decode(a)
	if err != nil {
		t.Fatal(err)
	}
	// The whole picture turned light: every cell's colours must follow at once.
	for i := 0; i < len(tf.FgYCbCr); i += 3 {
		if tf.FgYCbCr[i] < 200 && tf.BgYCbCr[i] < 200 {
			t.Fatalf("cell %d kept dark colours after the scene changed", i/3)
		}
	}
}

// An edge of moderate contrast that moves by one pixel must not be held.
func TestShapeHoldFollowsMovingEdge(t *testing.T) {
	const w, h = 64, 64
	frame := func(edge int) []byte {
		d := make([]byte, w*h*3)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				o := (y*w + x) * 3
				v := byte(100)
				if x >= edge {
					v = 160 // +60 luma: a soft but visible edge
				}
				d[o], d[o+1], d[o+2] = v, 128, 128
			}
		}
		return d
	}
	enc := NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(10)
	dec := NewDecoder()
	var prev []byte
	for edge := 29; edge <= 35; edge++ {
		out, err := enc.Encode(Frame{Data: frame(edge), Width: w, Height: h}, 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		tf, err := dec.Decode(out)
		if err != nil {
			t.Fatal(err)
		}
		cur := append(append([]byte(nil), tf.Glyphs...), tf.FgYCbCr...)
		cur = append(cur, tf.BgYCbCr...)
		if prev != nil && string(prev) == string(cur) {
			t.Fatalf("edge moved to x=%d but the picture did not change", edge)
		}
		prev = cur
	}
}

// The debug tint marks held cells but must not change which cells are held.
func TestHoldDebugTintsOnlyHeldCells(t *testing.T) {
	const w, h = 64, 64
	still := make([]byte, w*h*3)
	for i := range still {
		still[i] = byte(90 + i%7)
	}
	run := func(debug bool) (heldCount int, out []byte) {
		enc := NewEncoder(false, 0, 0, false)
		enc.SetShapeHold(10)
		enc.SetHoldDebug(debug)
		var last []byte
		for f := 0; f < 3; f++ {
			data, err := enc.Encode(Frame{Data: still, Width: w, Height: h}, 16, 8)
			if err != nil {
				t.Fatal(err)
			}
			last = data
		}
		for _, hld := range enc.held {
			if hld {
				heldCount++
			}
		}
		return heldCount, last
	}
	plain, _ := run(false)
	tinted, out := run(true)
	if plain != tinted || plain == 0 {
		t.Fatalf("held cells: %d without tint, %d with tint (want equal, > 0)", plain, tinted)
	}
	tf, err := NewDecoder().Decode(out)
	if err != nil {
		t.Fatal(err)
	}
	// A still grey picture: held cells must now carry the lilac tint (Cb > 128).
	if tf.BgYCbCr[1] <= 132 {
		t.Fatalf("held cell not tinted: Cb=%d", tf.BgYCbCr[1])
	}
}
