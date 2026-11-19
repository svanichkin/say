//go:build !darwin && !windows && !plan9

package gui

import "errors"

// Available reports whether this system has the pixel window.
func Available() bool { return false }

// Run is not available here yet: Linux needs X11 libraries at build time.
func Run(app func() error, in Input) error {
	return errors.New("the pixel window (-gui) is not available on this system yet")
}
