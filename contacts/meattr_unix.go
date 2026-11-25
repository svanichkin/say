//go:build darwin || linux

package contacts

import (
	"runtime"

	"golang.org/x/sys/unix"
)

func attrName() string {
	if runtime.GOOS == "linux" {
		return "user." + meAttr
	}
	return meAttr
}

// getMe reads a file's mark: the UID of our card in it.
func getMe(path string) (string, bool) {
	buf := make([]byte, 256)
	n, err := unix.Getxattr(path, attrName(), buf)
	if err != nil || n < 0 {
		return "", false
	}
	return string(buf[:n]), true
}

func setMe(path, uid string) error {
	if err := unix.Setxattr(path, attrName(), []byte(uid), 0); err != nil {
		if err == unix.ENOTSUP {
			return errNoAttr
		}
		return err
	}
	return nil
}

func clearMe(path string) error {
	if err := unix.Removexattr(path, attrName()); err != nil {
		if _, ok := getMe(path); ok {
			return err
		}
	}
	return nil
}
