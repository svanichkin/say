package codec

// Range-coded change frames (frameTypeDeltaRC, frameTypeDeltaPatchRC).
//
// A binary adaptive range coder (the LZMA scheme) codes, cell by cell, one
// "changed" bit and, for a changed cell, its glyph (6 bits) and its
// foreground and background palette indices (8 bits each) as bit trees.
// Every bit has an adaptive probability, so the coder learns the picture's
// statistics as the call goes. The changed bit is coded in the context of
// its neighbours (left, above-left, above, above-right) and of the same
// cell in the previous frame: changes come in patches and persist.
//
// The models restart at every frame that is not a change frame, on both
// sides: a receiver that lost a frame waits for the next full frame anyway,
// and continues from there with the encoder's state.

const (
	rcProbBits = 11
	rcProbOne  = 1 << rcProbBits
	rcMoveBits = 4
	rcTop      = 1 << 24
)

type rcEncoder struct {
	low       uint64
	rng       uint32
	cache     byte
	cacheSize int
	started   bool // the first byte the scheme emits is always 0: dropped
	out       []byte
}

func (e *rcEncoder) reset(buf []byte) {
	*e = rcEncoder{rng: 0xffffffff, cacheSize: 1, out: buf[:0]}
}

func (e *rcEncoder) shiftLow() {
	if uint32(e.low) < 0xff000000 || e.low>>32 != 0 {
		carry := byte(e.low >> 32)
		temp := e.cache
		for {
			if e.started {
				e.out = append(e.out, temp+carry)
			}
			e.started = true
			temp = 0xff
			e.cacheSize--
			if e.cacheSize == 0 {
				break
			}
		}
		e.cache = byte(uint32(e.low) >> 24)
	}
	e.cacheSize++
	e.low = (e.low & 0xffffff) << 8
}

func (e *rcEncoder) bit(p *uint16, b int) {
	bound := (e.rng >> rcProbBits) * uint32(*p)
	if b == 0 {
		e.rng = bound
		*p += (rcProbOne - *p) >> rcMoveBits
	} else {
		e.low += uint64(bound)
		e.rng -= bound
		*p -= *p >> rcMoveBits
	}
	for e.rng < rcTop {
		e.rng <<= 8
		e.shiftLow()
	}
}

// tree codes the low n bits of v, most significant first; probs has 1<<n
// entries (index 0 unused).
func (e *rcEncoder) tree(probs []uint16, n int, v int) {
	m := 1
	for i := n - 1; i >= 0; i-- {
		b := v >> i & 1
		e.bit(&probs[m], b)
		m = m<<1 | b
	}
}

// finish flushes the coder. Trailing zero bytes are dropped: the decoder
// reads zeros past the end. (So a truncated frame cannot be told from a
// complete one; a datagram arrives whole or not at all.)
func (e *rcEncoder) finish() []byte {
	for i := 0; i < 5; i++ {
		e.shiftLow()
	}
	out := e.out
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out
}

type rcDecoder struct {
	code uint32
	rng  uint32
	in   []byte
	pos  int
}

func (d *rcDecoder) next() byte {
	if d.pos >= len(d.in) {
		d.pos++
		return 0
	}
	b := d.in[d.pos]
	d.pos++
	return b
}

func (d *rcDecoder) reset(in []byte) {
	*d = rcDecoder{rng: 0xffffffff, in: in}
	for i := 0; i < 4; i++ { // the encoder dropped the leading zero
		d.code = d.code<<8 | uint32(d.next())
	}
}

func (d *rcDecoder) bit(p *uint16) int {
	bound := (d.rng >> rcProbBits) * uint32(*p)
	var b int
	if d.code < bound {
		d.rng = bound
		*p += (rcProbOne - *p) >> rcMoveBits
	} else {
		d.code -= bound
		d.rng -= bound
		*p -= *p >> rcMoveBits
		b = 1
	}
	for d.rng < rcTop {
		d.rng <<= 8
		d.code = d.code<<8 | uint32(d.next())
	}
	return b
}

func (d *rcDecoder) tree(probs []uint16, n int) int {
	m := 1
	for i := 0; i < n; i++ {
		m = m<<1 | d.bit(&probs[m])
	}
	return m - 1<<n
}

// rcModel is the adaptive state of the range-coded change frames.
type rcModel struct {
	valid   bool // false: restart before the next change frame
	changed [32]uint16
	glyph   [64]uint16
	// Colours in the context of the cell's previous colour's top bits: the
	// palette is sorted by colour code, brightness first, so these are
	// roughly the previous brightness, and a cell's brightness changes
	// little more often than not.
	fg   [8][256]uint16
	bg   [8][256]uint16
	prev []bool // cells that changed in the previous change frame
}

func (m *rcModel) restart(cells int) {
	probs := [][]uint16{m.changed[:], m.glyph[:]}
	for c := range m.fg {
		probs = append(probs, m.fg[c][:], m.bg[c][:])
	}
	for _, p := range probs {
		for i := range p {
			p[i] = rcProbOne / 2
		}
	}
	if cap(m.prev) < cells {
		m.prev = make([]bool, cells)
	}
	m.prev = m.prev[:cells]
	clear(m.prev)
	m.valid = true
}

// changedContext is the context of cell i's changed bit: which of its
// already coded neighbours changed in this frame, and whether it changed in
// the previous one.
func changedContext(changed, prev []bool, i, cols int) int {
	x := i % cols
	ctx := 0
	if x > 0 && changed[i-1] {
		ctx |= 1
	}
	if i >= cols {
		if x > 0 && changed[i-cols-1] {
			ctx |= 2
		}
		if changed[i-cols] {
			ctx |= 4
		}
		if x+1 < cols && changed[i-cols+1] {
			ctx |= 8
		}
	}
	if prev[i] {
		ctx |= 16
	}
	return ctx
}

// encode range-codes a change frame: for every cell the changed bit (from
// mask) and, for a changed cell, its glyph and colour indices; prevFg and
// prevBg are the cells' colours in the frame before.
func (m *rcModel) encode(enc *rcEncoder, glyphs, fg, bg, prevFg, prevBg, mask []byte, cols int, changed []bool) {
	for i := range glyphs {
		ch := mask[i/8]&(1<<(i%8)) != 0
		changed[i] = ch
		b := 0
		if ch {
			b = 1
		}
		enc.bit(&m.changed[changedContext(changed, m.prev, i, cols)], b)
		if !ch {
			continue
		}
		enc.tree(m.glyph[:], 6, int(glyphs[i]))
		enc.tree(m.fg[prevFg[i]>>5][:], 8, int(fg[i]))
		enc.tree(m.bg[prevBg[i]>>5][:], 8, int(bg[i]))
	}
	copy(m.prev, changed)
}

// decode applies a range-coded change frame to the base cells.
func (m *rcModel) decode(dec *rcDecoder, glyphs, fg, bg []byte, cols int, changed []bool) {
	for i := range glyphs {
		ch := dec.bit(&m.changed[changedContext(changed, m.prev, i, cols)]) == 1
		changed[i] = ch
		if !ch {
			continue
		}
		glyphs[i] = byte(dec.tree(m.glyph[:], 6))
		fg[i] = byte(dec.tree(m.fg[fg[i]>>5][:], 8)) // fg[i] is still the previous colour
		bg[i] = byte(dec.tree(m.bg[bg[i]>>5][:], 8))
	}
	copy(m.prev, changed)
}

// Range-coded full frames and keyframes (frameTypeFullRC, frameTypeKeyRC).
// A frame of its own: the model starts afresh with every such frame. Each
// field of a cell (glyph, foreground, background) is coded as "same as the
// cell to the left", else "same as the cell above", else its value; a
// picture is mostly even areas, where those flags cost a fraction of a bit.
type rcSpatial struct {
	eqLeft  [3][4]uint16 // per field; context: left == above, previous field matched
	eqAbove [3][2]uint16 // per field; context: previous field matched
	glyph   [64]uint16
	colour  [2][8][256]uint16 // fg, bg; context: top bits of the colour above (or left)
}

func (m *rcSpatial) restart() {
	probs := [][]uint16{m.glyph[:]}
	for f := range m.eqLeft {
		probs = append(probs, m.eqLeft[f][:], m.eqAbove[f][:])
	}
	for f := range m.colour {
		for c := range m.colour[f] {
			probs = append(probs, m.colour[f][c][:])
		}
	}
	for _, p := range probs {
		for i := range p {
			p[i] = rcProbOne / 2
		}
	}
}

// spatialCell describes what the coder of field f of cell i may refer to.
func spatialCell(plane []byte, i, cols int) (left, above int, hasLeft, hasAbove bool) {
	x := i % cols
	if x > 0 {
		left, hasLeft = int(plane[i-1]), true
	}
	if i >= cols {
		above, hasAbove = int(plane[i-cols]), true
	}
	return
}

// encode codes all cells of a frame.
func (m *rcSpatial) encode(enc *rcEncoder, glyphs, fg, bg []byte, cols int) {
	planes := [3][]byte{glyphs, fg, bg}
	for i := range glyphs {
		matched := false // the previous field of this cell repeated a neighbour
		for f, plane := range planes {
			v := int(plane[i])
			left, above, hasLeft, hasAbove := spatialCell(plane, i, cols)
			eq := false
			if hasLeft {
				ctx := 0
				if hasAbove && left == above {
					ctx |= 1
				}
				if matched {
					ctx |= 2
				}
				eq = v == left
				enc.bit(&m.eqLeft[f][ctx], b2i(eq))
			}
			if !eq && hasAbove && (!hasLeft || above != left) {
				eq = v == above
				enc.bit(&m.eqAbove[f][b2i(matched)], b2i(eq))
			}
			matched = eq
			if eq {
				continue
			}
			if f == 0 {
				enc.tree(m.glyph[:], 6, v)
				continue
			}
			ref := 0
			if hasAbove {
				ref = above
			} else if hasLeft {
				ref = left
			}
			enc.tree(m.colour[f-1][ref>>5][:], 8, v)
		}
	}
}

// decode fills all cells of a frame.
func (m *rcSpatial) decode(dec *rcDecoder, glyphs, fg, bg []byte, cols int) {
	planes := [3][]byte{glyphs, fg, bg}
	for i := range glyphs {
		matched := false
		for f, plane := range planes {
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
				if dec.bit(&m.eqLeft[f][ctx]) == 1 {
					v, eq = left, true
				}
			}
			if !eq && hasAbove && (!hasLeft || above != left) {
				if dec.bit(&m.eqAbove[f][b2i(matched)]) == 1 {
					v, eq = above, true
				}
			}
			matched = eq
			if !eq {
				if f == 0 {
					v = dec.tree(m.glyph[:], 6)
				} else {
					ref := 0
					if hasAbove {
						ref = above
					} else if hasLeft {
						ref = left
					}
					v = dec.tree(m.colour[f-1][ref>>5][:], 8)
				}
			}
			plane[i] = byte(v)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
