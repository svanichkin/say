//go:build plan9

package device

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// termSize reads the size vt(1) exports: $COLS and $LINES, rewritten on
// every resize. They are read from /env each time (the process's copy of
// the environment would keep the size at start).
func termSize() (cols, rows int, err error) {
	cols, rows = envInt("COLS"), envInt("LINES")
	if cols <= 0 || rows <= 0 {
		return 0, 0, errors.New("terminal size unknown: $COLS and $LINES are not set (run say in vt)")
	}
	return cols, rows, nil
}

func envInt(name string) int {
	v := os.Getenv(name)
	if b, err := os.ReadFile("/env/" + name); err == nil {
		v = string(b)
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimRight(v, "\x00")))
	if err != nil {
		return 0
	}
	return n
}
