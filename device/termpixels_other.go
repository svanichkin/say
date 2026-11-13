//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly)

package device

// TermPixelSize is the terminal's size in pixels: unknown here.
func TermPixelSize() (w, h int) { return 0, 0 }
