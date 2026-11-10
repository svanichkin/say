//go:build !windows

package main

// relaunchInWindowsTerminal is Windows-only (see relaunch_windows.go).
func relaunchInWindowsTerminal() bool { return false }
