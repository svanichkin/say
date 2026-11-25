package contacts

import (
	"os"
	"path/filepath"
	"runtime"
)

// Where the contacts are when nothing was chosen: the system's address
// book where there is one (macOS: Contacts), else the folder where the
// system keeps contacts or, where it keeps none, where they are usually
// kept. The choice is not written to the settings: only what the user
// chose is.

// DefaultFolder is the folder of cards when nothing was chosen (made when
// a card is first written to it).
func DefaultFolder() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "Contacts") // the Contacts folder Windows gives every user
	case "plan9":
		if h := os.Getenv("home"); h != "" {
			home = h
		}
		return filepath.Join(home, "lib", "contacts")
	case "darwin":
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, "say", "contacts") // without the Contacts book's access
		}
	}
	// Linux and the BSDs: a folder of cards as vdirsyncer and khard keep it.
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "contacts")
	}
	return filepath.Join(home, ".local", "share", "contacts")
}

// sourceLocked is where the contacts are now: "os" or "" (the folder,
// dirLocked).
func (s *Store) sourceLocked() string {
	if s.settings.Source != "" || s.settings.Dir != "" {
		return s.settings.Source
	}
	if OSBookAvailable() && OSBookStatus() != Denied {
		return "os"
	}
	return ""
}

// dirLocked is the folder of cards: chosen, else DefaultFolder.
func (s *Store) dirLocked() string {
	if s.settings.Dir != "" {
		return s.settings.Dir
	}
	return DefaultFolder()
}

// Access to the system's address book, as the system has it now.
const (
	NotAsked = iota
	Granted
	Denied
)
