//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package device

import (
	"os"

	"golang.org/x/sys/unix"
)

// TermPixelSize is the terminal's size in pixels (0, 0 when it does not
// say).
func TermPixelSize() (w, h int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return int(ws.Xpixel), int(ws.Ypixel)
}
