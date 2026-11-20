//go:build !(arm64 || amd64) || purego

package codec

func boxRowsSIMD(sum []uint16, src []byte, stride, rows int) int { return 0 }
