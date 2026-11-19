//go:build linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os"
	"os/exec"

	"golang.org/x/term"
)

// relaunchInTerminal reopens Say in a terminal window when it was started
// without a terminal in a graphical session: a double click in a file
// manager, a launcher without Terminal=true. Say has no window here (yet),
// so without a terminal nothing of it would show. It reports whether Say now
// runs there, so this process should exit.
//
// Nothing happens in a terminal, without a graphical session (a server, a
// service), when the terminal's own Say comes back here (SAY_RELAUNCHED), or
// when no terminal is found. The terminal is $TERMINAL, then the system's
// (x-terminal-emulator on Debian and its kin), then the common ones.
func relaunchInTerminal() bool {
	if term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	if os.Getenv("SAY_RELAUNCHED") != "" {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	cmd := append([]string{self}, os.Args[1:]...)
	for _, t := range terminalCommands(cmd) {
		path, err := exec.LookPath(t[0])
		if err != nil {
			continue
		}
		c := exec.Command(path, t[1:]...)
		c.Env = append(os.Environ(), "SAY_RELAUNCHED=1")
		if c.Start() != nil {
			continue
		}
		_ = c.Process.Release()
		return true
	}
	return false
}

// terminalCommands is how each terminal is asked to run cmd, in the order
// they are tried.
func terminalCommands(cmd []string) [][]string {
	with := func(prefix ...string) []string { return append(prefix, cmd...) }
	var list [][]string
	if t := os.Getenv("TERMINAL"); t != "" {
		list = append(list, with(t, "-e"))
	}
	return append(list,
		with("x-terminal-emulator", "-e"),
		with("gnome-terminal", "--"),
		with("konsole", "-e"),
		with("xfce4-terminal", "-x"),
		with("mate-terminal", "-x"),
		with("ptyxis", "--"),
		with("kgx", "--"),
		with("kitty"),
		with("ghostty", "-e"),
		with("alacritty", "-e"),
		with("wezterm", "start", "--"),
		with("foot"),
		with("xterm", "-e"),
	)
}
