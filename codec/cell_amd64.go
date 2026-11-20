//go:build amd64 && !purego

package codec

//go:noescape
func cellMaskSSE(block *cellBlock) uint32

//go:noescape
func cellSumsSSE(block *cellBlock, pattern uint32, sums *[6]uint32)

func cellMask(block *cellBlock) uint32 { return cellMaskSSE(block) }

func cellSums(block *cellBlock, pattern uint32, sums *[6]uint32) { cellSumsSSE(block, pattern, sums) }
