package conf

import "testing"

func TestSysAudioSwitch(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		want       bool
		wantTarget bool // the last argument is left as a dial target
	}{
		{[]string{"-sysaudio", "off"}, false, false},
		{[]string{"-sysaudio", "on"}, true, false},
		{[]string{"-sysaudio=false"}, false, false},
		{[]string{"-sysaudio"}, true, false},
		{[]string{"-sysaudio", "200:abcd::1"}, true, true},
	} {
		flags, consumed := collectDashPrefixedArgs(tc.args)
		opts := &AppOptions{SysAudio: !tc.want}
		if err := applyFlagTokens(flags, opts, &flagParseState{}); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if opts.SysAudio != tc.want {
			t.Fatalf("%v: SysAudio = %v, want %v", tc.args, opts.SysAudio, tc.want)
		}
		_, taken := consumed[len(tc.args)-1]
		if tc.wantTarget == taken {
			t.Fatalf("%v: last argument consumed = %v", tc.args, taken)
		}
	}
	if err := applyFlagTokens([]string{"-sysaudio=maybe"}, &AppOptions{}, &flagParseState{}); err == nil {
		t.Fatal("-sysaudio=maybe must be rejected")
	}
}
