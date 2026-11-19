//go:build darwin || windows

package gui

import (
	"os"
	"path/filepath"
	"testing"
)

// The place written is the place read back; a missing or broken file is no
// place, and the window opens as it did before.
func TestWindowPlaceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "say", "config-window.json")
	if _, ok := loadPlace(path); ok {
		t.Fatal("a place without a file")
	}
	want := windowPlace{X: -1200, Y: 40, W: 900, H: 700} // a monitor left of the main one
	if err := savePlace(path, want); err != nil {
		t.Fatal(err)
	}
	if got, ok := loadPlace(path); !ok || got != want {
		t.Fatalf("read back %+v (ok %v), want %+v", got, ok, want)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPlace(path); ok {
		t.Fatal("a broken file read as a place")
	}
	if _, ok := loadPlace(""); ok {
		t.Fatal("a place without a path")
	}
}
