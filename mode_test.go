package main

import (
	"os"
	"testing"

	"github.com/svanichkin/say/gui"
)

// Typed in a terminal, Say stays in it; started as a program (a double
// click, no terminal) it opens its window where there is one. -gui and -text
// decide; over SSH, and for -version and the like, the terminal.
func TestChooseGUI(t *testing.T) {
	args, asProgram := os.Args, startedAsProgram
	defer func() { os.Args, startedAsProgram = args, asProgram }()
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		t.Setenv(k, "")
	}
	program := false
	startedAsProgram = func() bool { return program }

	if chooseGUI(nil) {
		t.Fatal("typed in a terminal, the window opened")
	}
	if !chooseGUI([]string{"-gui"}) {
		t.Fatal("-gui in a terminal did not open the window")
	}
	program = true
	if got := chooseGUI(nil); got != gui.Available() {
		t.Fatalf("started as a program: window %v, want %v (where there is one)", got, gui.Available())
	}
	if chooseGUI([]string{"-text"}) {
		t.Fatal("-text opened the window")
	}
	if chooseGUI([]string{"-version"}) {
		t.Fatal("-version opened the window")
	}
	t.Setenv("SSH_CONNECTION", "10.0.0.1 50000 10.0.0.2 22")
	if chooseGUI(nil) {
		t.Fatal("over SSH the window opened")
	}
}

func TestDropProcessSerial(t *testing.T) {
	got := dropProcessSerial([]string{"/Applications/Say.app/Contents/MacOS/say", "-psn_0_12345", "-config", "client"})
	if len(got) != 3 || got[1] != "-config" || got[2] != "client" {
		t.Fatalf("%q", got)
	}
}

func TestInAppBundle(t *testing.T) {
	for path, want := range map[string]bool{
		"/Applications/Say.app/Contents/MacOS/say":     true,
		"/Users/me/say/bin/Say.app/Contents/MacOS/say": true,
		"/Users/me/say/bin/say":                        false,
		"/usr/local/bin/say":                           false,
		"/Users/me/MacOS/Contents/say":                 false,
		"/Users/me/notanapp/Contents/MacOS/say":        false,
	} {
		if got := inAppBundle(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}
