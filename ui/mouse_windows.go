//go:build windows

package ui

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// prepareTTYForMouse switches the console input into a VT-friendly raw mode so that
// SGR mouse escape sequences are delivered to stdin. The returned function restores
// the previous console mode when mouse tracking stops.
func prepareTTYForMouse(fd int) (func(), error) {
	handle := windows.Handle(uintptr(fd))
	if handle == windows.InvalidHandle {
		return nil, fmt.Errorf("stdin handle is invalid")
	}

	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		return nil, err
	}

	newMode := original
	newMode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_EXTENDED_FLAGS
	newMode &^= windows.ENABLE_QUICK_EDIT_MODE
	newMode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT

	if err := windows.SetConsoleMode(handle, newMode); err != nil {
		return nil, err
	}

	return func() {
		_ = windows.SetConsoleMode(handle, original)
	}, nil
}
