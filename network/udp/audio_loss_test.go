package udp

import "testing"

func TestAudioPacketsExpectedDTX(t *testing.T) {
	f := int32(audioFrameTicks)
	cases := []struct {
		name     string
		delta    int32
		afterSID bool
		missing  int32
	}{
		{"consecutive speech", 1 * f, false, 0},
		{"speech with two lost frames", 3 * f, false, 2},
		{"SID to SID update after 8 frames", 8 * f, true, 0},
		{"silence to speech after 5 frames", 5 * f, true, 0},
		{"silence to speech after 8 frames", 8 * f, true, 0},
		{"one SID update lost", 16 * f, true, 1},
		{"speech then SID with a lost frame", 2 * f, false, 1},
	}
	for _, c := range cases {
		if got := audioPacketsExpected(c.delta, c.afterSID) - 1; got != c.missing {
			t.Errorf("%s: missing %d, want %d", c.name, got, c.missing)
		}
	}
}
