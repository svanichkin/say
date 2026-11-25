package contacts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Settings are Say's own about the address book: where it is, how names
// are shown, who was called last, and our card in the system's book when
// another than the system's own was chosen. They are kept in a file beside the configuration
// (config-contacts.json for config.json), not in it: the Yggdrasil node
// rewrites the configuration from what it read at start.
type Settings struct {
	Dir    string `json:"dir,omitempty"`    // the folder of cards
	Source string `json:"source,omitempty"` // "os": the system's address book, not the folder
	// Me is our card in the system's book when it is not the one the
	// system has as ours (in a folder, the file marked "me" is).
	Me string `json:"me,omitempty"`
	// Display is how people are named in the list (contacts.Show*).
	Display string `json:"display,omitempty"` // our card's UID
	Last    string `json:"last,omitempty"`
	// LastHost is, per person (UID), the address of their device last
	// called or calling: the person's colour in the list.
	LastHost map[string]string `json:"last_host,omitempty"`

	path string
}

// Ready reports whether the contacts are set up: the system's book chosen,
// or a folder.
func (s *Settings) Ready() bool { return s.Source == "os" || s.Dir != "" }

// SettingsPath is the settings file for a configuration file.
func SettingsPath(configPath string) string {
	return strings.TrimSuffix(configPath, filepath.Ext(configPath)) + "-contacts.json"
}

// LoadSettings reads the settings (none yet: empty ones).
func LoadSettings(path string) (*Settings, error) {
	s := &Settings{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, s)
	s.path = path
	return s, err
}

// Save writes the settings.
func (s *Settings) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path is the settings file.
func (s *Settings) Path() string { return s.path }

// DefaultDir is the folder offered first: contacts beside the configuration
// (or beside its settings file).
func DefaultDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "contacts")
}

// Noted records a call with a person's device: they come first in the list
// next time, in that device's colour.
func (s *Settings) Noted(uid, host string) {
	if uid == "" {
		return
	}
	s.Last = uid
	if s.LastHost == nil {
		s.LastHost = map[string]string{}
	}
	s.LastHost[uid] = host
}
