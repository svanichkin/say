package codec

import "testing"

// A receiver that missed earlier frames must recover from the next
// periodic keyframe.
func TestPeriodicKeyframeLetsLateReceiverDecode(t *testing.T) {
	const w, h, interval = 64, 64, 10
	enc := NewEncoder(false, interval, 25, false)
	data := make([]byte, w*h*3)
	for i := range data {
		data[i] = byte(i * 13)
	}
	late := NewDecoder()
	decoded := 0
	for f := 0; f < 3*interval; f++ {
		data[f%len(data)] ^= 0x40 // small change: palette reuse would otherwise never key
		out, err := enc.Encode(Frame{Data: data, Width: w, Height: h}, 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		if f < 3 { // the first frames, including the initial keyframe, are lost
			continue
		}
		if _, err := late.Decode(out); err == nil {
			decoded++
		}
	}
	if decoded < 2*interval-2 {
		t.Fatalf("late receiver decoded only %d frames; periodic keyframes missing", decoded)
	}
}
