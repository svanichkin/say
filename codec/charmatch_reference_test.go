package codec

import "math/bits"

// The cell matcher written the plain way (per pixel, through getPixel, every
// bitmap tried), kept as the reference the optimized version must reproduce
// exactly.

func createCharDataReference(getPixel func(int, int) uint32, x0, y0, codePoint int, pattern uint32) CharData {
	var res CharData
	res.CodePoint = codePoint

	fgCount, bgCount := 0, 0
	mask := uint32(0x80000000)

	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			var avg *[3]int
			if pattern&mask != 0 {
				avg = &res.FgColor
				fgCount++
			} else {
				avg = &res.BgColor
				bgCount++
			}

			rgb := getPixel(x0+x, y0+y)
			for i := 0; i < 3; i++ {
				(*avg)[i] += getChannel(rgb, i)
			}
			mask >>= 1
		}
	}

	if bgCount > 0 {
		for i := range res.BgColor {
			res.BgColor[i] /= bgCount
		}
	}
	if fgCount > 0 {
		for i := range res.FgColor {
			res.FgColor[i] /= fgCount
		}
	}
	return res
}

func findCharDataReference(getPixel func(int, int) uint32, x0, y0, flags int) CharData {
	minC := [3]int{255, 255, 255}
	maxC := [3]int{0, 0, 0}

	// Cache the 4x8 block to avoid calling getPixel twice for each cell.
	var block [32]uint32
	idx := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			rgb := getPixel(x0+x, y0+y)
			block[idx] = rgb
			idx++
			for i := 0; i < 3; i++ {
				d := getChannel(rgb, i)
				if d < minC[i] {
					minC[i] = d
				}
				if d > maxC[i] {
					maxC[i] = d
				}
			}
		}
	}

	// Split along the widest channel (the first wins a tie) at its middle.
	splitIndex := 0
	bestSplit := 0
	for i := 0; i < 3; i++ {
		if maxC[i]-minC[i] > bestSplit {
			bestSplit = maxC[i] - minC[i]
			splitIndex = i
		}
	}
	splitValue := minC[splitIndex] + bestSplit/2

	var bitsVal uint32
	for _, px := range block {
		bitsVal <<= 1
		if getChannel(px, splitIndex) > splitValue {
			bitsVal |= 1
		}
	}

	bestDiff := 32
	bestPattern := uint32(0x0000ffff)
	codePoint := 0x2584

	for _, bm := range bitmaps {
		if bm.flags&flags != bm.flags {
			continue
		}
		pattern := bm.pattern
		for i := 0; i < 2; i++ {
			diff := bits.OnesCount32(pattern ^ bitsVal)
			if diff < bestDiff {
				bestDiff = diff
				bestPattern = bm.pattern
				codePoint = bm.codePoint
			}
			pattern = ^pattern
		}
	}

	return createCharDataReference(getPixel, x0, y0, codePoint, bestPattern)
}
