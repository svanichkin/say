//go:build windows

package device

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The clipboard through the Win32 API: running clip.exe or PowerShell from
// the window (a GUI program) opens a console window, and clip.exe garbles
// text outside the console's code page.

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// openClipboard opens the clipboard, retrying while another program holds it.
func openClipboard() error {
	var err error
	for i := 0; i < 10; i++ {
		var r uintptr
		r, _, err = procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

func setClipboard(text string) error {
	u, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return err
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return err
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h) // the clipboard did not take it
		return err
	}
	return nil
}

// ReadClipboard returns the clipboard's text ("" when there is none).
func ReadClipboard() string {
	if openClipboard() != nil {
		return ""
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p)))
}
