package codec

import "math/bits"

// The cell matcher as it was before the block-based rewrite, kept as the
// reference the optimized version must reproduce exactly.

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

	type colorCount struct {
		color uint32
		count int
	}

	var colors [32]colorCount
	nColors := 0

	// Cache the 4x8 block to avoid calling getPixel twice for each cell.
	var block [32]uint32
	idx := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			rgb := getPixel(x0+x, y0+y)
			block[idx] = rgb
			idx++

			var color uint32
			for i := 0; i < 3; i++ {
				d := getChannel(rgb, i)
				if d < minC[i] {
					minC[i] = d
				}
				if d > maxC[i] {
					maxC[i] = d
				}
				color = (color << 8) | uint32(d)
			}

			found := false
			for i := 0; i < nColors; i++ {
				if colors[i].color == color {
					colors[i].count++
					found = true
					break
				}
			}
			if !found && nColors < len(colors) {
				colors[nColors] = colorCount{color: color, count: 1}
				nColors++
			}
		}
	}

	var maxColor1, maxColor2 uint32
	count2 := 0

	if nColors > 0 {
		best1, best2 := -1, -1
		idx1, idx2 := -1, -1
		for i := 0; i < nColors; i++ {
			c := colors[i].count
			if c > best1 {
				best2, idx2 = best1, idx1
				best1, idx1 = c, i
			} else if c > best2 {
				best2, idx2 = c, i
			}
		}
		if idx1 >= 0 {
			maxColor1 = colors[idx1].color
			maxColor2 = maxColor1
			count2 = colors[idx1].count
		}
		if idx2 >= 0 {
			maxColor2 = colors[idx2].color
			count2 += colors[idx2].count
		}
	}

	var bitsVal uint32
	direct := count2 > (8*4)/2

	if direct {
		idx = 0
		for y := 0; y < 8; y++ {
			for x := 0; x < 4; x++ {
				bitsVal <<= 1
				rgb := block[idx]
				idx++
				d1, d2 := 0, 0
				for i := 0; i < 3; i++ {
					shift := uint(16 - 8*i)
					c1 := int((maxColor1 >> shift) & 0xff)
					c2 := int((maxColor2 >> shift) & 0xff)
					c := getChannel(rgb, i)
					d1 += (c1 - c) * (c1 - c)
					d2 += (c2 - c) * (c2 - c)
				}
				if d1 > d2 {
					bitsVal |= 1
				}
			}
		}
	} else {
		splitIndex := 0
		bestSplit := 0
		for i := 0; i < 3; i++ {
			if maxC[i]-minC[i] > bestSplit {
				bestSplit = maxC[i] - minC[i]
				splitIndex = i
			}
		}
		splitValue := minC[splitIndex] + bestSplit/2

		idx = 0
		for y := 0; y < 8; y++ {
			for x := 0; x < 4; x++ {
				bitsVal <<= 1
				if getChannel(block[idx], splitIndex) > splitValue {
					bitsVal |= 1
				}
				idx++
			}
		}
	}

	bestDiff := 32
	bestPattern := uint32(0x0000ffff)
	codePoint := 0x2584
	inverted := false

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
				inverted = bestPattern != pattern
			}
			pattern = ^pattern
		}
	}

	if direct {
		var res CharData
		if inverted {
			maxColor1, maxColor2 = maxColor2, maxColor1
		}
		for i := 0; i < 3; i++ {
			shift := uint(16 - 8*i)
			res.FgColor[i] = int((maxColor2 >> shift) & 0xff)
			res.BgColor[i] = int((maxColor1 >> shift) & 0xff)
		}
		res.CodePoint = codePoint
		return res
	}

	return createCharDataReference(getPixel, x0, y0, codePoint, bestPattern)
}
