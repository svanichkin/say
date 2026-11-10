//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
