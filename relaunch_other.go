//go:build !windows

package main

// hideConsole is Windows-only (see relaunch_windows.go).
func hideConsole() {}

// showError is Windows-only (see relaunch_windows.go).
func showError(string) {}

// doubleClicked is Windows-only: elsewhere a double click on a bare binary
// opens a terminal for it (macOS Finder), indistinguishable from typing it.
func doubleClicked() bool { return false }
