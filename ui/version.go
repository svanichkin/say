package ui

import "sync/atomic"

// The running build's version, shown small under the chat's input on the
// right: which Say this is should be seen at a glance (an old binary
// started by mistake looks the same otherwise).
var versionLabel atomic.Pointer[string]

// SetVersion sets the version shown under the chat's input.
func SetVersion(v string) { versionLabel.Store(&v) }

func shownVersion() string {
	if v := versionLabel.Load(); v != nil {
		return *v
	}
	return ""
}

// versionSGR is the version's colour: a dim grey.
func versionSGR() string {
	if ansiColors() {
		return "\x1b[1;30m" // vt(1)'s bright black
	}
	return "\x1b[38;2;110;110;110m"
}
