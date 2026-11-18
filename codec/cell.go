package codec

// A cell's mask and colour sums (findCharDataBlock), for every cell of a
// frame: in NEON on arm64 (cell_arm64.s), in Go elsewhere and with the
// purego tag; the same results.
//
// cellMask splits a cell along its widest channel (the first wins a tie) at
// the middle of its range: the pixels above it set, raster order from the
// top bit.
//
// cellSums writes the sums of Y, Cb and Cr under the set bits of pattern
// (sums[0:3]) and over the whole cell (sums[3:6]).
