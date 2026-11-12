//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// relaunchInWindowsTerminal reopens Say in Windows Terminal when it was
// started in a classic console of its own (a double click in Explorer). The
// classic console (conhost) has no font fallback: every character missing
// from its font, as many of the picture's block and line characters are,
// shows as a box. Windows Terminal falls back to fonts that have them, and
// draws coloured output much faster. It reports whether Say now runs there,
// so this process should exit.
//
// Nothing happens in Windows Terminal already (WT_SESSION), when the console
// is shared with a shell (Say was typed into cmd or PowerShell: that window
// stays the user's choice), or when Windows Terminal is not installed.
func relaunchInWindowsTerminal() bool {
	if os.Getenv("WT_SESSION") != "" || !ownsConsole() {
		return false
	}
	wt := findWindowsTerminal()
	if wt == "" {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	dir, _ := os.Getwd()
	// wt.exe splits its command line into subcommands at ';'.
	args := []string{"-d", dir, "--", self}
	for _, a := range os.Args[1:] {
		args = append(args, strings.ReplaceAll(a, ";", `\;`))
	}
	cmd := exec.Command(wt, args...)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return false
	}
	_ = cmd.Process.Release()
	return true
}

// ownsConsole reports whether this process is the only one attached to its
// console: Windows created the console for it.
func ownsConsole() bool {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if proc.Find() != nil {
		return false
	}
	var ids [4]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}

// findWindowsTerminal returns the path of wt.exe ("" if there is none).
func findWindowsTerminal() string {
	if p, err := exec.LookPath("wt.exe"); err == nil {
		return p
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		p := filepath.Join(local, "Microsoft", "WindowsApps", "wt.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// hideConsole closes the console window Windows opened for Say (a double
// click in Explorer) when Say goes on in its own window. A console shared
// with a shell stays.
func hideConsole() {
	if !ownsConsole() {
		return
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")
	if proc.Find() == nil {
		proc.Call()
	}
}

// fromExplorer is whether Say was started from Explorer: the console is
// its own. Taken at start, before hideConsole lets the console go.
var fromExplorer = ownsConsole()

// askModeDialog asks in a message box, not in the console, when Say was
// started from Explorer (the console is its own): two buttons, GUI (the
// window) and TUI (the terminal). asked is false elsewhere; then the
// console asks.
func askModeDialog() (window, asked bool) {
	if !fromExplorer {
		return false, false
	}
	const yesNo, yes = 0x4, 6
	runtime.LockOSThread() // the hook is for this thread's message box
	defer runtime.UnlockOSThread()
	unhook := relabelButtons(map[int]string{6: "GUI", 7: "TUI"}) // IDYES, IDNO
	ret := messageBox("Say", "", yesNo)
	unhook()
	if ret == 0 {
		return false, false
	}
	return ret == yes, true
}

// relabelButtons renames, by their ids, the buttons of the next message box
// this thread shows (a message box has no labels of its own choosing): a
// CBT hook catches the box as it is shown. It returns the unhook.
func relabelButtons(labels map[int]string) (unhook func()) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	setHook := user32.NewProc("SetWindowsHookExW")
	unhookProc := user32.NewProc("UnhookWindowsHookEx")
	next := user32.NewProc("CallNextHookEx")
	setText := user32.NewProc("SetDlgItemTextW")
	if setHook.Find() != nil || unhookProc.Find() != nil || next.Find() != nil || setText.Find() != nil {
		return func() {}
	}
	const whCBT, hcbtActivate = 5, 5
	var hook uintptr
	cb := windows.NewCallback(func(code, wparam, lparam uintptr) uintptr {
		if int32(code) == hcbtActivate {
			for id, label := range labels {
				if p, err := windows.UTF16PtrFromString(label); err == nil {
					setText.Call(wparam, uintptr(id), uintptr(unsafe.Pointer(p)))
				}
			}
		}
		r, _, _ := next.Call(hook, code, wparam, lparam)
		return r
	})
	hook, _, _ = setHook.Call(whCBT, cb, 0, uintptr(windows.GetCurrentThreadId()))
	return func() {
		if hook != 0 {
			unhookProc.Call(hook)
			hook = 0
		}
	}
}

// showError shows a message box when Say was started from Explorer (the
// error printed to the console would vanish with it).
func showError(text string) {
	if !fromExplorer {
		return
	}
	const iconError = 0x10
	messageBox("Say", text, iconError)
}

func messageBox(title, text string, flags uintptr) uintptr {
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	if proc.Find() != nil {
		return 0
	}
	t, err1 := windows.UTF16PtrFromString(title)
	m, err2 := windows.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		return 0
	}
	ret, _, _ := proc.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
	return ret
}
