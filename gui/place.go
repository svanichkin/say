//go:build darwin || windows

package gui

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// The window opens where it was closed, as large: its place is kept in a
// file (Input.Place) beside the configuration, so each configuration's window
// keeps its own. It is written once the window has stayed put for
// placeSettle after a move or a resize, and when it closes; not while it is
// minimized or full screen, whose place is not the window's.

const placeSettle = time.Second

type windowPlace struct {
	X, Y, W, H int
}

// loadPlace reads the place kept in path (ok false: none, or unusable).
func loadPlace(path string) (p windowPlace, ok bool) {
	if path == "" {
		return p, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &p) != nil || p.W <= 0 || p.H <= 0 {
		return p, false
	}
	return p, true
}

// savePlace writes p to path, through a temporary file so that a crash
// mid-write leaves the place before.
func savePlace(path string, p windowPlace) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// currentPlace is the window's place now (ok false: minimized or full
// screen).
func currentPlace() (p windowPlace, ok bool) {
	if ebiten.IsWindowMinimized() || ebiten.IsFullscreen() {
		return p, false
	}
	p.X, p.Y = ebiten.WindowPosition()
	p.W, p.H = ebiten.WindowSize()
	return p, p.W > 0 && p.H > 0
}

// placeKeeper notes the window's moves and writes the place once it settles.
type placeKeeper struct {
	path    string
	saved   windowPlace
	seen    windowPlace
	changed time.Time // when seen last changed; zero: nothing to write
}

// update is called every tick: it writes the place once it has stayed put.
func (k *placeKeeper) update(now time.Time) {
	if k.path == "" {
		return
	}
	p, ok := currentPlace()
	if !ok {
		return
	}
	if p != k.seen {
		k.seen, k.changed = p, now
		return
	}
	if !k.changed.IsZero() && now.Sub(k.changed) >= placeSettle {
		k.write(p)
	}
}

// flush writes the place now (the window is closing).
func (k *placeKeeper) flush() {
	if k.path == "" {
		return
	}
	if p, ok := currentPlace(); ok {
		k.write(p)
	}
}

func (k *placeKeeper) write(p windowPlace) {
	k.changed = time.Time{}
	if p == k.saved {
		return
	}
	if err := savePlace(k.path, p); err != nil {
		log.Printf("[gui] the window's place not kept: %v", err)
		return
	}
	k.saved = p
}
