//go:build !windows

package main

// relaunchInWindowsTerminal is Windows-only (see relaunch_windows.go).
func relaunchInWindowsTerminal() bool { return false }

// hideConsole is Windows-only (see relaunch_windows.go).
func hideConsole() {}

// askModeDialog and showError are Windows-only (see relaunch_windows.go).
func askModeDialog() (window, asked bool) { return false, false }
func showError(string)                    {}
