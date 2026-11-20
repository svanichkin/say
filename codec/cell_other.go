//go:build !(arm64 || amd64) || purego

package codec

func cellMask(block *cellBlock) uint32 { return cellMaskGo(block) }

func cellSums(block *cellBlock, pattern uint32, sums *[6]uint32) { cellSumsGo(block, pattern, sums) }
