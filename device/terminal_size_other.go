//go:build !plan9

package device

import (
	"os"

	"golang.org/x/term"
)

func termSize() (cols, rows int, err error) {
	return term.GetSize(int(os.Stdout.Fd()))
}
