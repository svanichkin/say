package ui

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestDroppedFileFormats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "My File (1).txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(dir, `My\ File\ \(1\).txt`)
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	for name, text := range map[string]string{
		"plain":                           path,
		"Terminal/iTerm2 escaped + space": escaped + " ",
		"double quoted":                   `"` + path + `"`,
		"single quoted":                   "'" + path + "'",
		"file URI":                        uri,
	} {
		got, ok := droppedFile(text)
		if !ok || got != path {
			t.Errorf("%s: %q -> %q, %v; want %q", name, text, got, ok, path)
		}
	}
	for _, text := range []string{"hello world", filepath.Join(dir, "missing.txt"), dir} {
		if _, ok := droppedFile(text); ok {
			t.Errorf("%q treated as a file", text)
		}
	}
}
