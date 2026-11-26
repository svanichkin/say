//go:build windows

package gui

import "github.com/hajimehoshi/ebiten/v2"

// On Windows the window goes to the taskbar (with no icon of its own in a
// tray it could not come back otherwise).
func hideApp() { ebiten.MinimizeWindow() }

func showApp() {
	if ebiten.IsWindowMinimized() {
		ebiten.RestoreWindow()
	}
}
