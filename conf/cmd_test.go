package conf

import (
	"os"
	"path/filepath"
	"testing"
)

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
	flags, _ := collectDashPrefixedArgs([]string{"-camera", "/dev/video2", "200:abcd::1"})
	opts := &AppOptions{}
	if err := applyFlagTokens(flags, opts, &flagParseState{}); err != nil || opts.Camera != "/dev/video2" {
		t.Fatalf("-camera /dev/video2: %q, %v", opts.Camera, err)
	}
	if err := applyFlagTokens([]string{"-sysaudio=maybe"}, &AppOptions{}, &flagParseState{}); err == nil {
		t.Fatal("-sysaudio=maybe must be rejected")
	}
}

// -config is found in any of its forms before the arguments are parsed.
func TestConfigPathFromArgs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg") // used on Linux; macOS and Windows have their own
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(base, "say")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, filepath.Join(dir, "config.json")},
		{[]string{"-config", "client", "-port", "7001"}, filepath.Join(dir, "client.json")},
		{[]string{"--config=server"}, filepath.Join(dir, "server.json")},
		{[]string{"-nolan", "-config", "/tmp/x.json", "200:abcd::1"}, "/tmp/x.json"},
	} {
		if got := ConfigPathFromArgs(tc.args); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.args, got, tc.want)
		}
	}
}
