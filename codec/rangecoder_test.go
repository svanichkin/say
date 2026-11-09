package codec

import (
	"errors"
	"math/rand"
	"testing"
)

func TestRangeCoderRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		n := rng.Intn(5000)
		bias := rng.Float64()
		type sym struct {
			kind int // 0 bit, 1 tree6, 2 tree8
			ctx  int
			v    int
		}
		syms := make([]sym, n)
		for i := range syms {
			switch k := rng.Intn(3); k {
			case 0:
				b := 0
				if rng.Float64() < bias {
					b = 1
				}
				if trial%5 == 0 { // long runs exercise the carry
					b = (i / 300) & 1
				}
				syms[i] = sym{0, rng.Intn(4), b}
			case 1:
				syms[i] = sym{1, 0, rng.Intn(64) & int(0x3f>>uint(rng.Intn(4)))}
			default:
				syms[i] = sym{2, 0, rng.Intn(256)}
			}
		}
		var enc rcEncoder
		enc.reset(nil)
		var bits [4]uint16
		var t6 [64]uint16
		var t8 [256]uint16
		init := func() {
			for _, p := range [][]uint16{bits[:], t6[:], t8[:]} {
				for i := range p {
					p[i] = rcProbOne / 2
				}
			}
		}
		init()
		for _, s := range syms {
			switch s.kind {
			case 0:
				enc.bit(&bits[s.ctx], s.v)
			case 1:
				enc.tree(t6[:], 6, s.v)
			default:
				enc.tree(t8[:], 8, s.v)
			}
		}
		data := enc.finish()
		init()
		var dec rcDecoder
		dec.reset(data)
		for i, s := range syms {
			var got int
			switch s.kind {
			case 0:
				got = dec.bit(&bits[s.ctx])
			case 1:
				got = dec.tree(t6[:], 6)
			default:
				got = dec.tree(t8[:], 8)
			}
			if got != s.v {
				t.Fatalf("trial %d symbol %d (kind %d): got %d, want %d", trial, i, s.kind, got, s.v)
			}
		}
	}
}

// frameKind returns the frame type of a payload, zstd-wrapped or not.
func frameKind(t *testing.T, out []byte) byte {
	t.Helper()
	if string(out[:len(PayloadMagic)]) == PayloadMagic {
		return out[len(PayloadMagic)]
	}
	return frameType(t, out)
}

func TestRangeCodedFramesReproduceTheSentPicture(t *testing.T) {
	for _, src := range []struct {
		name   string
		frames [][]byte
	}{{"noisy", noisyFrames(60, 3)}, {"drifting", driftingFrames(60)}} {
		enc := NewEncoder(false, 20, 0, false)
		enc.SetShapeHold(10)
		enc.SetPalettePatches(true)
		enc.SetRangeCoding(true)
		dec := NewDecoder()
		types := map[byte]int{}
		for f, frame := range src.frames {
			out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
			if err != nil {
				t.Fatal(err)
			}
			typ := out[len(PayloadMagic)]
			if string(out[:len(PayloadMagic)]) != PayloadMagic {
				typ = frameType(t, out) // zstd-wrapped
			}
			types[typ]++
			if _, err := dec.Decode(out); err != nil {
				t.Fatalf("%s frame %d (type %d): %v", src.name, f, typ, err)
			}
			assertSameAsSent(t, enc, dec, f)
		}
		if types[frameTypeDeltaRC]+types[frameTypeDeltaPatchRC] == 0 || types[frameTypeKeyRC] == 0 {
			t.Fatalf("%s: range-coded change frames or keyframes missing: %v", src.name, types)
		}
		if types[frameTypeKey]+types[frameTypeFull]+types[frameTypeDelta]+types[frameTypeDeltaPatch] != 0 {
			t.Fatalf("%s: zstd frames with range coding on: %v", src.name, types)
		}
		t.Logf("%s: frame types %v", src.name, types)
	}
}

func TestLostRangeCodedFrameResyncsAtTheNextFullFrame(t *testing.T) {
	enc := NewEncoder(false, 15, 0, false)
	enc.SetShapeHold(10)
	enc.SetPalettePatches(true)
	enc.SetRangeCoding(true)
	dec := NewDecoder()
	lost, recovered := false, false
	for f, frame := range noisyFrames(80, 5) {
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		typ := out[len(PayloadMagic)]
		if string(out[:len(PayloadMagic)]) != PayloadMagic {
			typ = frameType(t, out)
		}
		rc := typ == frameTypeDeltaRC || typ == frameTypeDeltaPatchRC // a change frame
		if !lost && rc && f > 8 {
			lost = true
			dec.ResetBase()
			continue
		}
		_, err = dec.Decode(out)
		if lost && !recovered {
			if !rc {
				if err != nil {
					t.Fatalf("frame %d: full frame after the loss: %v", f, err)
				}
				recovered = true
				assertSameAsSent(t, enc, dec, f)
				continue
			}
			if !errors.Is(err, ErrNoBase) {
				t.Fatalf("frame %d after the loss: %v, want ErrNoBase", f, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
		assertSameAsSent(t, enc, dec, f)
	}
	if !lost || !recovered {
		t.Fatalf("lost %v, recovered %v", lost, recovered)
	}
}

func TestCorruptRangeCodedFrameIsRefused(t *testing.T) {
	enc := NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(10)
	enc.SetRangeCoding(true)
	frames := noisyFrames(6, 9)
	var payloads [][]byte
	for _, frame := range frames {
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, out)
	}
	k := -1
	for i, p := range payloads {
		if string(p[:len(PayloadMagic)]) == PayloadMagic {
			k = i
		}
	}
	if k < 0 {
		t.Fatal("no range-coded frame")
	}
	last := payloads[k]
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		dec := NewDecoder()
		for _, p := range payloads[:k] {
			if _, err := dec.Decode(p); err != nil {
				t.Fatal(err)
			}
		}
		bad := append([]byte(nil), last...)
		switch trial % 2 {
		case 0: // truncated
			bad = bad[:len(PayloadMagic)+5+rng.Intn(max(1, len(bad)-len(PayloadMagic)-5))]
		default: // flipped bytes
			for k := 0; k < 3; k++ {
				i := len(PayloadMagic) + 5 + rng.Intn(len(bad)-len(PayloadMagic)-5)
				bad[i] ^= byte(1 + rng.Intn(255))
			}
		}
		tf, err := dec.Decode(bad) // must not panic; garbage is acceptable only as an error or a valid-sized frame
		if err == nil && (tf.Cols != 88 || tf.Rows != 36) {
			t.Fatalf("trial %d: decoded %dx%d", trial, tf.Cols, tf.Rows)
		}
	}
}

// A frame whose code ends in a long run of zero bytes loses them all (the
// decoder reads zeros past the end) and must still decode.
func TestRangeCoderTrailingZeros(t *testing.T) {
	for n := 0; n < 3000; n += 7 {
		var enc rcEncoder
		enc.reset(nil)
		p := uint16(rcProbOne / 2)
		var probs [64]uint16
		for i := range probs {
			probs[i] = rcProbOne / 2
		}
		for i := 0; i < n; i++ {
			enc.bit(&p, 0) // ever more probable zeros: tiny code, often ending in zeros
		}
		enc.tree(probs[:], 6, n%64)
		data := enc.finish()
		p = rcProbOne / 2
		for i := range probs {
			probs[i] = rcProbOne / 2
		}
		var dec rcDecoder
		dec.reset(data)
		for i := 0; i < n; i++ {
			if dec.bit(&p) != 0 {
				t.Fatalf("n=%d: bit %d decoded as 1", n, i)
			}
		}
		if got := dec.tree(probs[:], 6); got != n%64 {
			t.Fatalf("n=%d: tree %d, want %d", n, got, n%64)
		}
	}
}

// Scene cuts without palette changes produce full frames without a palette
// (frameTypeFullRC); they must decode with the last keyframe's palette.
func TestRangeCodedFullFrames(t *testing.T) {
	a, b := noisyFrames(1, 11)[0], noisyFrames(1, 11)[0]
	for i := range b { // the same colours, moved: a cut, but no new palette
		b[i] = a[len(a)-1-i/3*3+i%3-2]
	}
	enc := NewEncoder(false, 0, 0, false)
	enc.SetRangeCoding(true)
	dec := NewDecoder()
	types := map[byte]int{}
	for f := 0; f < 12; f++ {
		frame := a
		if f%2 == 1 {
			frame = b
		}
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		types[frameKind(t, out)]++
		if _, err := dec.Decode(out); err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
		assertSameAsSent(t, enc, dec, f)
	}
	if types[frameTypeFullRC] == 0 {
		t.Fatalf("no full frames without a palette: %v", types)
	}
	t.Logf("frame types %v", types)
}

func TestForceKeyframe(t *testing.T) {
	for _, rc := range []bool{false, true} {
		enc := NewEncoder(false, 0, 0, false)
		enc.SetShapeHold(10)
		enc.SetRangeCoding(rc)
		frames := noisyFrames(6, 4)
		for f, frame := range frames {
			if f == 3 {
				enc.ForceKeyframe()
			}
			out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
			if err != nil {
				t.Fatal(err)
			}
			key := frameKind(t, out) == frameTypeKey || frameKind(t, out) == frameTypeKeyRC
			if want := f == 0 || f == 3; key != want {
				t.Fatalf("range coding %v, frame %d: keyframe %v, want %v", rc, f, key, want)
			}
		}
	}
}
