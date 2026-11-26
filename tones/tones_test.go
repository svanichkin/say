package tones

import (
	"encoding/binary"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var preview = flag.String("preview", "", "write the tones (a few periods each) as WAV files to this folder")

// The tones are long enough, loud enough, never clipped (Ring loops, Fail
// plays once). With -preview=<dir> they are written to listen to.
func TestTones(t *testing.T) {
	for _, tc := range []struct {
		name string
		pcm  func() []int16
	}{{"ring", Ring}, {"fail", Fail}} {
		pcm := tc.pcm()
		if float64(len(pcm))/Rate < 0.5 {
			t.Fatalf("%s: %.2f s", tc.name, float64(len(pcm))/Rate)
		}
		peak := 0
		for _, v := range pcm {
			peak = max(peak, int(v), -int(v))
		}
		if peak < 10000 || peak > 32000 {
			t.Fatalf("%s: peak %d", tc.name, peak)
		}
		if *preview != "" {
			var loop []int16
			for n := 0; n < 3 || len(loop) < 6*Rate; n++ { // three rounds, six seconds at least
				if n > 0 && strings.HasPrefix(tc.name, "fail") {
					break // played once
				}
				loop = append(loop, pcm...)
			}
			if err := writeWAV(filepath.Join(*preview, tc.name+".wav"), loop); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeWAV(path string, pcm []int16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n := uint32(len(pcm) * 2)
	hdr := []any{
		[4]byte{'R', 'I', 'F', 'F'}, 36 + n, [4]byte{'W', 'A', 'V', 'E'},
		[4]byte{'f', 'm', 't', ' '}, uint32(16), uint16(1), uint16(1), uint32(Rate), uint32(Rate * 2), uint16(2), uint16(16),
		[4]byte{'d', 'a', 't', 'a'}, n,
	}
	for _, v := range hdr {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	return binary.Write(f, binary.LittleEndian, pcm)
}
