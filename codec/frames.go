package codec

// The frames' range-coded models (rangecoder.go). After its header (magic,
// type, cols, rows) a frame is one range-coded stream:
//
//   - frameTypeKey: the palette, then every cell;
//   - frameTypeFull: every cell, on the palette the receiver holds;
//   - frameTypeDelta: a changed bit per cell and the changed cells' fields;
//   - frameTypeDeltaPatch: palette entries replaced, then as frameTypeDelta.
//
// The palette (codes Y6 Cb5 Cr5) goes as the differences of neighbouring
// codes, Exp-Golomb with adaptive bits: about 140 bytes for 256 colours.
// One built for a key frame ascends; one that patches changed in place (a
// periodic key frame resends it) takes signed differences.
//
// A cell's fields are its glyph, background and foreground; the empty
// glyph shows its background only, so its foreground is not coded and takes
// the background's index. In key and full frames each field is "as the
// cell to the left", else "as the cell above", else its value; in change
// frames a changed cell's field is first "as it was". A colour index is
// coded bit by bit against a reference index (the cell above in key and
// full frames, the cell's previous colour in change frames): while the bits
// so far match the reference's, the next bit's context is the reference's
// next bit, so near indices (similar colours: the palette is sorted by
// brightness first) cost little.
//
// In change frames a changed cell's glyph, when coded as a value, is coded
// in the model of the glyph the cell had before (64 of them: a shape mostly
// turns into a near one): 6-15% fewer bits for those glyphs, 1-2% smaller
// frames on camera video (cmd/videosim), as fast.
//
// The key and full frames' model starts afresh with each such frame; the
// change frames' model carries on from one change frame to the next and
// restarts after a key or full frame, on both sides: a receiver that lost
// a frame waits for the next key or full frame anyway.
//
// Against the earlier formats (protocol 4: the palette raw, colours in the
// context of the reference's top bits, the empty glyph's foreground coded):
// stills 10-28% smaller, sequences 8-16%. Tried and left out: glyphs in the
// context of empty left and upper cells (+0.1%), the foreground predicted
// from the cell's background (±0.3%), slower adaptation (+1 to +7%).

import "math/bits"

// rcCoder is the encoder or the decoder (one of them nil), so both sides
// run the same code: the value given is ignored when decoding.
type rcCoder struct {
	e *rcEncoder
	d *rcDecoder
}

func (c rcCoder) bit(p *uint16, b int) int {
	if c.e != nil {
		c.e.bit(p, b)
		return b
	}
	return c.d.bit(p)
}

func (c rcCoder) tree(probs []uint16, n, v int) int {
	if c.e != nil {
		c.e.tree(probs, n, v)
		return v & (1<<n - 1)
	}
	return c.d.tree(probs, n)
}

// expGolomb codes v ≥ 1: its length (lens: 32 entries), then the bits
// below the top one (bits: 18 x 17).
func (c rcCoder) expGolomb(lens []uint16, bitsP *[18][17]uint16, v int) int {
	n := min(17, c.tree(lens, 5, max(0, bits.Len(uint(max(1, v)))-1))+1)
	r := 1
	for i := n - 2; i >= 0; i-- {
		r = r<<1 | c.bit(&bitsP[n][i], v>>i&1)
	}
	return r
}

// colour codes palette index v against ref (-1: none): its 8 bits, most
// significant first, each in the context of the bits before it and, while
// they follow ref's, of ref's bit.
func (c rcCoder) colour(pred *[3][256]uint16, v, ref int) int {
	if c.e != nil {
		c.e.colour(pred, v, ref)
		return v & 255
	}
	return c.d.colour(pred, ref)
}

func halve(p []uint16) {
	for i := range p {
		p[i] = rcProbOne / 2
	}
}

// rcFrame is the model of a key or full frame.
type rcFrame struct {
	palN    [256]uint16
	palUp   uint16 // the palette ascends
	palLen  [32]uint16
	palBits [18][17]uint16
	eqLeft  [3][4]uint16 // per field; context: left == above, previous field matched
	eqAbove [3][2]uint16 // per field; context: previous field matched
	glyph   [64]uint16
	pred    [2][3][256]uint16 // fg, bg
}

func (m *rcFrame) restart() {
	halve(m.palN[:])
	m.palUp = rcProbOne / 2
	halve(m.palLen[:])
	for a := range m.palBits {
		halve(m.palBits[a][:])
	}
	for f := range m.eqLeft {
		halve(m.eqLeft[f][:])
		halve(m.eqAbove[f][:])
	}
	halve(m.glyph[:])
	for f := range m.pred {
		for k := range m.pred[f] {
			halve(m.pred[f][k][:])
		}
	}
}

// palette codes the palette's codes (1 to 256 of them); the decoder passes
// nil and gets them.
func (m *rcFrame) palette(c rcCoder, codes []uint16) []uint16 {
	n := c.tree(m.palN[:], 8, len(codes)-1) + 1
	up := 0
	if c.e != nil && codesAscend(codes) {
		up = 1
	}
	up = c.bit(&m.palUp, up)
	if c.d != nil {
		codes = make([]uint16, n)
	}
	prev := -1
	if up == 0 {
		prev = 0
	}
	for i := range codes {
		var v int
		if up == 1 {
			v = prev + c.expGolomb(m.palLen[:], &m.palBits, int(codes[i])-prev)
		} else {
			// Signed, zigzag: 0, -1, 1, -2, ... as 1, 2, 3, 4, ...
			d := int(codes[i]) - prev
			z := c.expGolomb(m.palLen[:], &m.palBits, d<<1^d>>63+1) - 1
			v = prev + (z>>1 ^ -(z & 1))
		}
		v = max(0, min(0xffff, v))
		codes[i] = uint16(v)
		prev = v
	}
	return codes
}

// The cells of a key or full frame: every cell's glyph, then its
// background, then its foreground (none for the empty glyph: it takes the
// background). Each field first says whether it repeats the cell to the
// left (in the context of left == above and of the field before
// repeating), else whether it repeats the cell above (where it differs
// from the left one), else codes its value: the glyph by its 6 bits, a
// colour against the one above (or to the left). encodeCells and
// decodeCells are this one model, the coder's state in registers; they
// must stay mirror images.

// encodeCells codes the cells.
func (m *rcFrame) encodeCells(e *rcEncoder, glyphs, fg, bg []byte, cols int) {
	planes := [3][]byte{glyphs, bg, fg}
	rng, low := e.rng, e.low
	for i := range glyphs {
		matched := false // the previous field of this cell repeated a neighbour
		for f, plane := range planes {
			if f == 2 && glyphs[i] == 0 {
				continue
			}
			left, above, hasLeft, hasAbove := spatialCell(plane, i, cols)
			v, eq := int(plane[i]), false
			if hasLeft {
				ctx := 0
				if hasAbove && left == above {
					ctx |= 1
				}
				if matched {
					ctx |= 2
				}
				eq = v == left
				if rng, low = encStep(rng, low, &m.eqLeft[f][ctx], b2i(eq)); rng < rcTop {
					e.rng, e.low = rng, low
					e.normalize()
					rng, low = e.rng, e.low
				}
			}
			if !eq && hasAbove && (!hasLeft || above != left) {
				eq = v == above
				if rng, low = encStep(rng, low, &m.eqAbove[f][b2i(matched)], b2i(eq)); rng < rcTop {
					e.rng, e.low = rng, low
					e.normalize()
					rng, low = e.rng, e.low
				}
			}
			matched = eq
			if !eq {
				e.rng, e.low = rng, low
				if f == 0 {
					e.tree(m.glyph[:], 6, v)
				} else {
					ref := -1
					if hasAbove {
						ref = above
					} else if hasLeft {
						ref = left
					}
					e.colour(&m.pred[2-f], v, ref)
				}
				rng, low = e.rng, e.low
			}
		}
	}
	e.rng, e.low = rng, low
}

// decodeCells decodes the cells into glyphs, fg and bg.
func (m *rcFrame) decodeCells(d *rcDecoder, glyphs, fg, bg []byte, cols int) {
	planes := [3][]byte{glyphs, bg, fg}
	rng, code := d.rng, d.code
	for i := range glyphs {
		matched := false
		for f, plane := range planes {
			if f == 2 && glyphs[i] == 0 {
				fg[i] = bg[i]
				continue
			}
			left, above, hasLeft, hasAbove := spatialCell(plane, i, cols)
			v, eq := 0, false
			if hasLeft {
				ctx := 0
				if hasAbove && left == above {
					ctx |= 1
				}
				if matched {
					ctx |= 2
				}
				var b int
				if rng, code, b = decStep(rng, code, &m.eqLeft[f][ctx]); rng < rcTop {
					d.rng, d.code = rng, code
					d.normalize()
					rng, code = d.rng, d.code
				}
				if eq = b == 1; eq {
					v = left
				}
			}
			if !eq && hasAbove && (!hasLeft || above != left) {
				var b int
				if rng, code, b = decStep(rng, code, &m.eqAbove[f][b2i(matched)]); rng < rcTop {
					d.rng, d.code = rng, code
					d.normalize()
					rng, code = d.rng, d.code
				}
				if eq = b == 1; eq {
					v = above
				}
			}
			matched = eq
			if !eq {
				d.rng, d.code = rng, code
				if f == 0 {
					v = d.tree(m.glyph[:], 6)
				} else {
					ref := -1
					if hasAbove {
						ref = above
					} else if hasLeft {
						ref = left
					}
					v = d.colour(&m.pred[2-f], ref)
				}
				rng, code = d.rng, d.code
			}
			plane[i] = byte(v)
		}
	}
	d.rng, d.code = rng, code
}

// rcChange is the model of the change frames.
type rcChange struct {
	valid     bool // false: restart before the next change frame
	changed   [32]uint16
	same      [3][2]uint16 // field as before; context: the previous field was
	eqLeft    [3][2]uint16 // context: left == above
	eqAbove   [3]uint16
	glyph     [64][64]uint16 // by the cell's glyph before
	pred      [2][3][256]uint16
	patchN    [256]uint16
	patchSlot [256]uint16
	patchHi   [256]uint16
	patchLo   [256]uint16
	prev      []bool // cells that changed in the previous change frame
}

func (m *rcChange) restart(cells int) {
	halve(m.changed[:])
	for f := range m.same {
		halve(m.same[f][:])
		halve(m.eqLeft[f][:])
	}
	halve(m.eqAbove[:])
	for g := range m.glyph {
		halve(m.glyph[g][:])
	}
	for f := range m.pred {
		for k := range m.pred[f] {
			halve(m.pred[f][k][:])
		}
	}
	for _, p := range [][]uint16{m.patchN[:], m.patchSlot[:], m.patchHi[:], m.patchLo[:]} {
		halve(p)
	}
	if cap(m.prev) < cells {
		m.prev = make([]bool, cells)
	}
	m.prev = m.prev[:cells]
	clear(m.prev)
	m.valid = true
}

// patch codes a palette patch: entries (slot, code lo, hi), 1 to 256 of
// them; the decoder passes nil and gets it.
func (m *rcChange) patch(c rcCoder, p []byte) []byte {
	n := c.tree(m.patchN[:], 8, len(p)/3-1) + 1
	if c.d != nil {
		p = make([]byte, 3*n)
	}
	for k := 0; k < 3*n; k += 3 {
		p[k] = byte(c.tree(m.patchSlot[:], 8, int(p[k])))
		p[k+2] = byte(c.tree(m.patchHi[:], 8, int(p[k+2])))
		p[k+1] = byte(c.tree(m.patchLo[:], 8, int(p[k+1])))
	}
	return p
}

// cells codes a change frame's cells: a changed bit per cell (the encoder's
// from mask) in the context of its neighbours and of the previous change
// frame, and a changed cell's fields. The encoder's planes hold this
// frame and prevG, prevB, prevF the frame before; the decoder's hold the
// frame before and are brought to this one.
func (m *rcChange) cells(c rcCoder, glyphs, fg, bg, prevG, prevF, prevB, mask []byte, cols int, changed []bool) {
	planes := [3][]byte{glyphs, bg, fg}
	for i := range glyphs {
		ch := c.e != nil && mask[i/8]&(1<<(i%8)) != 0
		ch = c.bit(&m.changed[changedContext(changed, m.prev, i, cols)], b2i(ch)) == 1
		changed[i] = ch
		if !ch {
			continue
		}
		prevs := [3]byte{glyphs[i], bg[i], fg[i]}
		if c.e != nil {
			prevs = [3]byte{prevG[i], prevB[i], prevF[i]}
		}
		same := false
		for f, plane := range planes {
			if f == 2 && glyphs[i] == 0 {
				fg[i] = bg[i]
				continue
			}
			p, v := int(prevs[f]), int(plane[i])
			if same = c.bit(&m.same[f][b2i(same)], b2i(v == p)) == 1; same {
				plane[i] = byte(p)
				continue
			}
			left, above, hasLeft, hasAbove := spatialCell(plane, i, cols)
			eq := false
			if hasLeft && left != p {
				if eq = c.bit(&m.eqLeft[f][b2i(hasAbove && left == above)], b2i(v == left)) == 1; eq {
					v = left
				}
			}
			if !eq && hasAbove && above != p && (!hasLeft || above != left) {
				if eq = c.bit(&m.eqAbove[f], b2i(v == above)) == 1; eq {
					v = above
				}
			}
			if !eq {
				if f == 0 {
					v = c.tree(m.glyph[p&63][:], 6, v)
				} else {
					v = c.colour(&m.pred[2-f], v, p)
				}
			}
			plane[i] = byte(v)
		}
	}
	copy(m.prev, changed)
}

// codesAscend reports whether codes ascend strictly.
func codesAscend(codes []uint16) bool {
	for i := 1; i < len(codes); i++ {
		if codes[i] <= codes[i-1] {
			return false
		}
	}
	return true
}

// paletteCodes are the 16-bit codes (Y6 Cb5 Cr5) of palette entries in
// codes (the encoder's palette).
func paletteCodes(palette []paletteColor) []uint16 {
	codes := make([]uint16, len(palette))
	for i, p := range palette {
		codes[i] = uint16(p[0]&0x3f)<<10 | uint16(p[1]&0x1f)<<5 | uint16(p[2]&0x1f)
	}
	return codes
}

// expandCode is the colour (Y, Cb, Cr) of a 16-bit palette code.
func expandCode(v uint16) paletteColor {
	e := expandPaletteEntry(paletteColor{byte(v >> 10 & 0x3f), byte(v >> 5 & 0x1f), byte(v & 0x1f)})
	return paletteColor{byte(e[0]), byte(e[1]), byte(e[2])}
}
