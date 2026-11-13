//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package conf

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile holds an exclusive flock on path while the file stays open.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
