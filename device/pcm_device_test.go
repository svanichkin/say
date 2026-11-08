package device

import (
	"bytes"
	"testing"
)

func TestParseAudioSpeed(t *testing.T) {
	cases := map[string]int{
		"audio out 65\nspeed 48000\ndelay 1764\n":                        48000,
		"dev /dev/audio\nmix 100 100\nfmtout s16c2r44100\nspeed 22050\n": 22050,
		"audio out 65\n": defaultDeviceRate,
		"speed x\n":      defaultDeviceRate,
		"":               defaultDeviceRate,
	}
	for in, want := range cases {
		if got := parseAudioSpeed(in); got != want {
			t.Errorf("parseAudioSpeed(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestStereoS16(t *testing.T) {
	b := appendStereoS16(nil, []float32{1, -2, 40000})
	want := []byte{1, 0, 1, 0, 0xfe, 0xff, 0xfe, 0xff, 0xff, 0x7f, 0xff, 0x7f}
	if !bytes.Equal(b, want) {
		t.Fatalf("appendStereoS16 = % x, want % x", b, want)
	}
	// L=100, R=-300 and a trailing partial frame.
	mono := appendMonoFromStereoS16(nil, []byte{100, 0, 0xd4, 0xfe, 1, 2, 3})
	if len(mono) != 1 || mono[0] != -100 {
		t.Fatalf("appendMonoFromStereoS16 = %v, want [-100]", mono)
	}
}
