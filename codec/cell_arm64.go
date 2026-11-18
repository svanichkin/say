//go:build arm64 && !purego

package codec

//go:noescape
func cellMaskNEON(block *cellBlock) uint32

//go:noescape
func cellSumsNEON(block *cellBlock, pattern uint32, sums *[6]uint32)

func cellMask(block *cellBlock) uint32 { return cellMaskNEON(block) }

func cellSums(block *cellBlock, pattern uint32, sums *[6]uint32) { cellSumsNEON(block, pattern, sums) }
