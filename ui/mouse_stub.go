//go:build windows

package ui

import "fmt"

func prepareTTYForMouse(fd int) (func(), error) {
	return nil, fmt.Errorf("mouse capture not supported on Windows")
}
