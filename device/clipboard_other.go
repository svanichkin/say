//go:build !windows

package device

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

func setClipboard(string) error { return errors.New("no clipboard API") }

// ReadClipboard returns the clipboard's text ("" when there is none).
func ReadClipboard() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}
