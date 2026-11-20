//go:build arm64 && !purego

package codec

import "unsafe"

//go:noescape
func boxRowsNEON(sum *uint16, src *byte, stride, rows, n int)

// boxRowsSIMD does boxRows for the first bytes of sum in sixteens and
// returns how many.
func boxRowsSIMD(sum []uint16, src []byte, stride, rows int) int {
	n := len(sum) &^ 15
	if n > 0 {
		boxRowsNEON(unsafe.SliceData(sum), unsafe.SliceData(src), stride, rows, n)
	}
	return n
}
