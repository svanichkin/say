//go:build windows

package device

import (
	"os"

	"golang.org/x/sys/windows"
)

const utf8CodePage = 65001

// supportsSyncOutput indicates whether the terminal backend supports the ANSI
// synchronized output sequence (DCS Ps = 1 s ... ST). Windows consoles do not,
// so this is always false on this platform.
const supportsSyncOutput = false

// init enables virtual terminal processing on stdout and stderr so that ANSI
// escape sequences are interpreted correctly on Windows consoles.
func init() {
	enableVirtualTerminalProcessing()
	forceUTF8ConsoleEncoding()
}

// enableVirtualTerminalProcessing configures stdout and stderr to use
// Virtual Terminal (VT) mode, enabling ANSI escape sequence support in
// PowerShell, Windows Terminal, and legacy console hosts that support it.
func enableVirtualTerminalProcessing() {
	// iterate over stdout and stderr and enable VT mode when possible
	handles := []windows.Handle{
		windows.Handle(os.Stdout.Fd()),
		windows.Handle(os.Stderr.Fd()),
	}

	for _, h := range handles {
		if h == windows.InvalidHandle {
			continue
		}

		// read the current console mode; skip if this is not a console handle
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err != nil {
			continue
		}

		// enable VT processing and processed output; disable automatic CRLF return
		mode |= windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		mode &^= windows.DISABLE_NEWLINE_AUTO_RETURN

		_ = windows.SetConsoleMode(h, mode)
	}
}

// forceUTF8ConsoleEncoding switches the active console input/output code pages
// to UTF-8 so Unicode glyphs render correctly without requiring users to run
// "chcp 65001" manually.
func forceUTF8ConsoleEncoding() {
	// Code page 65001 (UTF-8) is supported on modern Windows; ignore errors if unavailable.
	_ = windows.SetConsoleOutputCP(utf8CodePage)
	_ = windows.SetConsoleCP(utf8CodePage)
}
