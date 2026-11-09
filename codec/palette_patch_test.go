package codec

import (
	"errors"
	"testing"
)

// driftingFrames: 22x18 tiles of 16x16 pixels whose colours drift a little
// every frame, so the palette keeps missing colours.
func driftingFrames(n int) [][]byte {
	const w, h = 352, 288
	out := make([][]byte, n)
	for f := range out {
		d := make([]byte, w*h*3)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				tx, ty := x/16, y/16
				o := (y*w + x) * 3
				d[o] = byte(30 + (tx*9+ty*5+f*3)%200)
				d[o+1] = byte(60 + (tx*7+f*5)%140)
				d[o+2] = byte(60 + (ty*11+f*4)%140)
				if (x/4+y/8)%3 == 0 { // some structure for the glyphs
					d[o] = byte(int(d[o]) / 2)
				}
			}
		}
		out[f] = d
	}
	return out
}

func frameType(t *testing.T, out []byte) byte {
	t.Helper()
	raw, err := decompressZstd(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw[len(PayloadMagic)]
}

func TestPalettePatchesReproduceTheSentPicture(t *testing.T) {
	frames := driftingFrames(60)
	keys := func(patches bool) int {
		enc := NewEncoder(false, 0, 0, false)
		enc.SetPalettePatches(patches)
		dec := NewDecoder()
		n, patched := 0, 0
		for f, frame := range frames {
			out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
			if err != nil {
				t.Fatal(err)
			}
			switch frameType(t, out) {
			case frameTypeKey:
				n++
			case frameTypeDeltaPatch:
				patched++
			}
			if _, err := dec.Decode(out); err != nil {
				t.Fatalf("patches %v, frame %d: %v", patches, f, err)
			}
			assertSameAsSent(t, enc, dec, f)
			if len(dec.lastPalette) != len(enc.lastPalette) {
				t.Fatalf("frame %d: palette of %d entries, encoder has %d", f, len(dec.lastPalette), len(enc.lastPalette))
			}
			for i, c := range enc.lastPalette {
				e := expandPaletteEntry(c)
				if got := dec.lastPalette[i]; int(got[0]) != e[0] || int(got[1]) != e[1] || int(got[2]) != e[2] {
					t.Fatalf("frame %d: palette entry %d is %v, encoder has %v", f, i, got, e)
				}
			}
		}
		if patches && patched == 0 {
			t.Fatal("no frame patched the palette")
		}
		return n
	}
	without, with := keys(false), keys(true)
	if with >= without {
		t.Fatalf("keyframes with patches %d, without %d: patches should replace them", with, without)
	}
	t.Logf("keyframes: %d without patches, %d with", without, with)
}

func TestLostPalettePatchWaitsForThePalette(t *testing.T) {
	enc := NewEncoder(false, 20, 0, false)
	enc.SetPalettePatches(true)
	dec := NewDecoder()
	lost, recovered := false, false
	sincePatch := false
	for f, frame := range driftingFrames(80) {
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		typ := frameType(t, out)
		switch typ {
		case frameTypeDeltaPatch:
			sincePatch = true
		case frameTypeKey:
			sincePatch = false
		case frameTypeFull:
			if sincePatch {
				t.Fatalf("frame %d: a frame without the palette follows a patch", f)
			}
		}
		if !lost && typ == frameTypeDeltaPatch && f > 5 {
			lost = true // the network drops it
			dec.ResetBase()
			continue
		}
		_, err = dec.Decode(out)
		if lost && !recovered {
			if typ == frameTypeKey {
				if err != nil {
					t.Fatalf("frame %d: keyframe after the loss: %v", f, err)
				}
				recovered = true
				assertSameAsSent(t, enc, dec, f)
				continue
			}
			if !errors.Is(err, ErrNoBase) {
				t.Fatalf("frame %d (type %d) after a lost patch: %v, want ErrNoBase", f, typ, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
	}
	if !lost || !recovered {
		t.Fatalf("lost %v, recovered %v", lost, recovered)
	}
}

// Switching to brightness only starts afresh: no grey may be matched to an
// old tinted palette entry, and no held cell may keep its colour.
func TestSetBWLeavesNoColour(t *testing.T) {
	enc := NewEncoder(false, 0, 0, false)
	enc.SetPalettePatches(true)
	enc.SetShapeHold(64)
	dec := NewDecoder()
	frames := driftingFrames(12)
	for f, frame := range frames {
		if f == 6 {
			enc.SetBW(true)
		}
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		tf, err := dec.Decode(out)
		if err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
		if f == 6 && frameType(t, out) != frameTypeKey {
			t.Fatalf("switch frame type %d, want a keyframe", frameType(t, out))
		}
		if f < 6 {
			continue
		}
		for c, g := range tf.Glyphs {
			p := glyphPatterns[g]
			// Only colours that are drawn: a cell all background (or all
			// foreground) leaves the other colour unused.
			for _, pl := range [][]byte{tf.FgYCbCr, tf.BgYCbCr} {
				if (p == 0 && &pl[0] == &tf.FgYCbCr[0]) || (p == 0xffffffff && &pl[0] == &tf.BgYCbCr[0]) {
					continue
				}
				if pl[c*3+1] != 128 || pl[c*3+2] != 128 {
					t.Fatalf("frame %d: cell %d (pattern %08x) has colour (Cb %d, Cr %d) after SetBW", f, c, p, pl[c*3+1], pl[c*3+2])
				}
			}
		}
	}
}
