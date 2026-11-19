//go:build darwin || windows

package gui

import (
	"bytes"
	"embed"
	"image"
	"image/png"

	"github.com/hajimehoshi/ebiten/v2"
)

// The window's own icon, drawn by cmd/sayicon (make icons): Windows shows it
// in the title bar and the taskbar; macOS takes the app bundle's and ignores
// this.
//
//go:embed icon/*.png
var iconFiles embed.FS

func setWindowIcon() {
	var icons []image.Image
	for _, name := range []string{"icon-16.png", "icon-32.png", "icon-48.png", "icon-256.png"} {
		data, err := iconFiles.ReadFile("icon/" + name)
		if err != nil {
			continue
		}
		if img, err := png.Decode(bytes.NewReader(data)); err == nil {
			icons = append(icons, img)
		}
	}
	if len(icons) > 0 {
		ebiten.SetWindowIcon(icons)
	}
}
