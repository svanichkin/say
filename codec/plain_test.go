package codec

import (
	"math/rand"
	"reflect"
	"testing"
)

// The self view skips zstd: EncodePlain/DecodePlain must show exactly what
// Encode/Decode show, frame after frame (keyframes and change frames).
func TestPlainMatchesCompressed(t *testing.T) {
	const w, h, cols, rows = 352, 288, 88, 36
	rng := rand.New(rand.NewSource(11))
	base := make([]byte, w*h*3)
	for i := range base {
		base[i] = byte(rng.Intn(256))
	}
	zEnc, pEnc := NewEncoder(false, 10, 30, true), NewEncoder(false, 10, 30, true)
	zEnc.SetShapeHold(10)
	pEnc.SetShapeHold(10)
	zDec, pDec := NewDecoder(), NewDecoder()
	for f := 0; f < 40; f++ {
		data := make([]byte, len(base))
		for i := range data {
			data[i] = byte(min(255, max(0, int(base[i])+rng.Intn(9)-4+f%5)))
		}
		frame := Frame{Data: data, Width: w, Height: h}
		z, err := zEnc.Encode(frame, cols, rows)
		if err != nil {
			t.Fatal(err)
		}
		p, err := pEnc.EncodePlain(frame, cols, rows)
		if err != nil {
			t.Fatal(err)
		}
		zf, err := zDec.Decode(z)
		if err != nil {
			t.Fatal(err)
		}
		pf, err := pDec.DecodePlain(p)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(zf, pf) {
			t.Fatalf("frame %d differs between plain and compressed", f)
		}
	}
}
