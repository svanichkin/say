//go:build !windows && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package main

// relaunchInTerminal: on macOS the bare say runs in the terminal it was
// started in (a double click opens Terminal for it), and Say.app opens its
// window; Plan 9 has its window system's terminals.
func relaunchInTerminal() bool { return false }
