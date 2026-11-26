//go:build !darwin

package gui

// TrayAvailable reports whether Say can live in the tray here: not yet
// (Windows' notification area is to come).
func TrayAvailable() bool { return false }

// SetTray does nothing here (TrayAvailable).
func SetTray(on bool, items []TrayItem, pick func(id int)) { _ = tray.pick }

func catchQuit() {}

func quitAsked() bool { return false }
