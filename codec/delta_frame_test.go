package codec

import (
	"errors"
	"math/rand"
	"testing"
)

func noisyFrames(n int, seed int64) [][]byte {
	const w, h = 352, 288
	rng := rand.New(rand.NewSource(seed))
	base := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 3
			base[o], base[o+1], base[o+2] = byte(40+(x*3+y*2)%180), byte(110+x%30), byte(120+y%25)
		}
	}
	out := make([][]byte, n)
	for f := range out {
		d := make([]byte, len(base))
		for i := range d {
			d[i] = byte(min(255, max(0, int(base[i])+rng.Intn(13)-6)))
		}
		if f%7 == 3 { // something moves now and then
			for y := 100; y < 160; y++ {
				for x := 100 + f; x < 160+f; x++ {
					o := (y*352 + x%352) * 3
					d[o] = 250
				}
			}
		}
		out[f] = d
	}
	return out
}

func assertSameAsSent(t *testing.T, enc *Encoder, dec *Decoder, frame int) {
	t.Helper()
	if string(dec.baseGlyphs) != string(enc.sentGlyphs) || string(dec.baseFg) != string(enc.sentFg) || string(dec.baseBg) != string(enc.sentBg) {
		t.Fatalf("frame %d: decoded cells differ from the cells the encoder sent", frame)
	}
}

func TestDeltaFramesReproduceTheSentPicture(t *testing.T) {
	enc := NewEncoder(false, 10, 0, false)
	enc.SetShapeHold(10)
	dec := NewDecoder()
	types := map[byte]int{}
	var fullBytes, deltaBytes, fulls, deltas int
	for f, frame := range noisyFrames(40, 1) {
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Decode(out); err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
		raw, _ := decompressZstd(out)
		typ := raw[len(PayloadMagic)]
		types[typ]++
		if typ == frameTypeDelta {
			deltaBytes += len(out)
			deltas++
		} else {
			fullBytes += len(out)
			fulls++
		}
		assertSameAsSent(t, enc, dec, f)
	}
	if deltas == 0 || fulls == 0 {
		t.Fatalf("frame types %v: want both full and change frames", types)
	}
	t.Logf("full frames: %d, avg %d bytes; change frames: %d, avg %d bytes", fulls, fullBytes/fulls, deltas, deltaBytes/deltas)
	if deltaBytes/deltas*5 > fullBytes/fulls {
		t.Fatalf("change frames are not much smaller than full frames")
	}
}

func TestDeltaFramesRecoverAfterLoss(t *testing.T) {
	enc := NewEncoder(false, 10, 0, false)
	enc.SetShapeHold(10)
	dec := NewDecoder()
	recovered := false
	for f, frame := range noisyFrames(30, 2) {
		out, err := enc.Encode(Frame{Data: frame, Width: 352, Height: 288}, 88, 36)
		if err != nil {
			t.Fatal(err)
		}
		if f == 12 { // this packet is lost in the network
			dec.ResetBase()
			continue
		}
		raw, _ := decompressZstd(out)
		typ := raw[len(PayloadMagic)]
		_, err = dec.Decode(out)
		switch {
		case f > 12 && !recovered && typ == frameTypeDelta:
			if !errors.Is(err, ErrNoBase) {
				t.Fatalf("frame %d: change frame after a loss decoded (%v), want ErrNoBase", f, err)
			}
		case err != nil:
			t.Fatalf("frame %d: %v", f, err)
		default:
			if f > 12 {
				recovered = true
			}
			assertSameAsSent(t, enc, dec, f)
		}
	}
	if !recovered {
		t.Fatal("never recovered after the lost frame")
	}
}
