//go:build windows

package contacts

import (
	"errors"
	"os"
)

// The mark is an alternate data stream of the file (NTFS): file.vcf:me.
func stream(path string) string { return path + ":" + meAttr }

func getMe(path string) (string, bool) {
	b, err := os.ReadFile(stream(path))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func setMe(path, uid string) error {
	if err := os.WriteFile(stream(path), []byte(uid), 0o644); err != nil {
		return errNoAttr
	}
	return nil
}

func clearMe(path string) error {
	if err := os.Remove(stream(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
