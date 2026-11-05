package udp

import (
	"math"
	"math/rand"
	"testing"
)

// speechLike returns seconds of an 8 kHz voice-like signal: a gliding pitch
// with harmonics, syllable-rate loudness and some breath noise.
func speechLike(seconds int) []int16 {
	n := audioSampleRate * seconds
	out := make([]int16, n)
	rng := rand.New(rand.NewSource(1))
	phase := 0.0
	for i := range out {
		t := float64(i) / audioSampleRate
		f0 := 140 + 40*math.Sin(2*math.Pi*0.7*t)
		phase += 2 * math.Pi * f0 / audioSampleRate
		v := 0.0
		for k := 1; k <= 20 && float64(k)*f0 < 3800; k++ {
			v += math.Sin(float64(k)*phase) / float64(k)
		}
		env := math.Max(0, math.Sin(2*math.Pi*2.5*t)) // syllables with pauses
		out[i] = int16(6000*env*v + 300*rng.NormFloat64())
	}
	return out
}

func benchmarkCodec(b *testing.B, name string, pcm []int16) {
	enc, dec, frame, err := initAudioCodec(name)
	if err != nil {
		b.Fatal(err)
	}
	frames := len(pcm) / frame
	buf := make([]byte, maxAudioPacket)
	out := make([]int16, frame*2)
	b.Run("encode", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			f := i % frames
			if _, err := enc.EncodeFrame(pcm[f*frame:(f+1)*frame], buf); err != nil {
				b.Fatal(err)
			}
		}
		reportCPU(b, frame)
	})
	packets := make([][]byte, frames)
	total := 0
	for f := range packets {
		n, err := enc.EncodeFrame(pcm[f*frame:(f+1)*frame], buf)
		if err != nil {
			b.Fatal(err)
		}
		packets[f] = append([]byte(nil), buf[:n]...)
		total += n
	}
	b.Run("decode", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			p := packets[i%frames]
			var err error
			if len(p) < 2 { // DTX sent nothing: the receiver conceals
				_, err = dec.Conceal(out)
			} else {
				_, err = dec.DecodeFrame(p, out)
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		reportCPU(b, frame)
		b.ReportMetric(float64(total)*8/float64(len(pcm))*audioSampleRate/1000, "kbit/s")
	})
}

// reportCPU reports the share of one core the codec takes in real time.
func reportCPU(b *testing.B, frame int) {
	perFrame := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
	frameNs := float64(frame) * 1e9 / audioSampleRate
	b.ReportMetric(perFrame/frameNs*100, "%core")
}

func BenchmarkAudioCodec(b *testing.B) {
	speech := speechLike(10)
	silence := make([]int16, len(speech))
	for _, name := range []string{"opus", "g722", "amrnb"} {
		b.Run(name+"/speech", func(b *testing.B) { benchmarkCodec(b, name, speech) })
		b.Run(name+"/silence", func(b *testing.B) { benchmarkCodec(b, name, silence) })
	}
}
