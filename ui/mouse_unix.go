//go:build !windows

package ui

import "golang.org/x/sys/unix"

func prepareTTYForMouse(fd int) (func(), error) {
	orig, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	newState := *orig
	newState.Lflag &^= unix.ICANON | unix.ECHO
	newState.Iflag &^= unix.ICRNL | unix.INLCR | unix.IXON
	newState.Cc[unix.VMIN] = 1
	newState.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &newState); err != nil {
		return nil, err
	}
	return func() {
		_ = unix.IoctlSetTermios(fd, ioctlSetTermios, orig)
	}, nil
}
