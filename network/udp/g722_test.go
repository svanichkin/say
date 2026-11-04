package udp

import (
	"math"
	"testing"
)

func TestG722RoundTrip(t *testing.T) {
	enc, dec, frameSamples, err := initAudioCodec("g722")
	if err != nil {
		t.Fatal(err)
	}
	if frameSamples != 160 {
		t.Fatalf("frameSamples = %d, want 160", frameSamples)
	}
	pcm := make([]int16, frameSamples)
	out := make([]int16, frameSamples)
	buf := make([]byte, maxAudioPacket)
	var errSum, sigSum float64
	phase := 0.0
	for f := 0; f < 50; f++ { // 1 s of 440 Hz
		for i := range pcm {
			pcm[i] = int16(8000 * math.Sin(phase))
			phase += 2 * math.Pi * 440 / 8000
		}
		n, err := enc.EncodeFrame(pcm, buf)
		if err != nil || n <= 0 || n > maxAudioPacket {
			t.Fatalf("encode: %d bytes, %v", n, err)
		}
		m, err := dec.DecodeFrame(buf[:n], out)
		// The packed bitstream carries one codeword into the next packet, so
		// the very first frame yields one sample less (as in v0.1.3).
		if err != nil || m < frameSamples-1 || (f > 0 && m != frameSamples) {
			t.Fatalf("decode: %d samples, %v", m, err)
		}
		if f == 10 {
			t.Logf("packet %d bytes per 20 ms (%d kbit/s)", n, n*8*50/1000)
		}
		if f >= 10 {
			for i := range pcm {
				sigSum += float64(pcm[i]) * float64(pcm[i])
			}
			// The codec delays the signal; compare energy, not samples.
			for i := range out {
				errSum += float64(out[i]) * float64(out[i])
			}
		}
	}
	if ratio := errSum / sigSum; ratio < 0.5 || ratio > 2 {
		t.Fatalf("decoded energy is %.2f of the input", ratio)
	}
	first, _ := dec.Conceal(out)
	a := rms(out[:first])
	dec.Conceal(out)
	if b := rms(out); !(b < a && a > 0) {
		t.Fatalf("concealment does not fade: %.0f then %.0f", a, b)
	}
}

func rms(s []int16) float64 {
	acc := 0.0
	for _, v := range s {
		acc += float64(v) * float64(v)
	}
	return math.Sqrt(acc / float64(max(len(s), 1)))
}
