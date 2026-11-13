//go:build plan9

package conf

import (
	"errors"
	"os"
)

// lockFile keeps path open; the file is exclusive-use (DMEXCL), so the file
// server refuses to open it again while it is.
func lockFile(path string) (func(), error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, os.ModeExclusive|0o600)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		// Plan 9 says "file in use" (or "exclusive lock") for a held one.
		if _, statErr := os.Stat(path); statErr == nil {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
