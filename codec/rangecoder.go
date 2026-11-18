package codec

// A binary adaptive range coder (the LZMA scheme): every bit has an
// adaptive probability, so the coder learns a frame's statistics as it
// goes. The frames' models are in frames.go.

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

// encStep codes bit b at probability *p on the coder's range and low,
// adapting *p, and returns them (normalize follows when rng < rcTop): no
// call in it, so it inlines into the loops below, which keep rng and low
// in registers over a symbol's bits.
func encStep(rng uint32, low uint64, p *uint16, b int) (uint32, uint64) {
	v := uint32(*p)
	bound := (rng >> rcProbBits) * v
	if b == 0 {
		rng = bound
		v += (rcProbOne - v) >> rcMoveBits
	} else {
		low += uint64(bound)
		rng -= bound
		v -= v >> rcMoveBits
	}
	*p = uint16(v)
	return rng, low
}

// normalize brings the range back to rcTop or above.
func (e *rcEncoder) normalize() {
	for e.rng < rcTop {
		e.rng <<= 8
		e.shiftLow()
	}
}

func (e *rcEncoder) bit(p *uint16, b int) {
	e.rng, e.low = encStep(e.rng, e.low, p, b)
	if e.rng < rcTop {
		e.normalize()
	}
}

// tree codes the low n bits of v, most significant first; probs has 1<<n
// entries (index 0 unused).
func (e *rcEncoder) tree(probs []uint16, n int, v int) {
	rng, low := e.rng, e.low
	m := 1
	for i := n - 1; i >= 0; i-- {
		b := v >> i & 1
		if rng, low = encStep(rng, low, &probs[m], b); rng < rcTop {
			e.rng, e.low = rng, low
			e.normalize()
			rng, low = e.rng, e.low
		}
		m = m<<1 | b
	}
	e.rng, e.low = rng, low
}

// colour codes palette index v against ref (-1: none), as rcCoder.colour
// describes.
func (e *rcEncoder) colour(pred *[3][256]uint16, v, ref int) {
	rng, low := e.rng, e.low
	m, on := 1, ref >= 0
	for i := 7; i >= 0; i-- {
		s := 0
		if on {
			s = 1 + ref>>i&1
		}
		b := v >> i & 1
		if rng, low = encStep(rng, low, &pred[s][m], b); rng < rcTop {
			e.rng, e.low = rng, low
			e.normalize()
			rng, low = e.rng, e.low
		}
		if on && b != ref>>i&1 {
			on = false
		}
		m = m<<1 | b
	}
	e.rng, e.low = rng, low
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

// decStep decodes a bit at probability *p from the coder's range and code,
// adapting *p (as encStep).
func decStep(rng, code uint32, p *uint16) (uint32, uint32, int) {
	v := uint32(*p)
	bound := (rng >> rcProbBits) * v
	b := 0
	if code < bound {
		rng = bound
		v += (rcProbOne - v) >> rcMoveBits
	} else {
		code -= bound
		rng -= bound
		v -= v >> rcMoveBits
		b = 1
	}
	*p = uint16(v)
	return rng, code, b
}

// normalize brings the range back to rcTop or above.
func (d *rcDecoder) normalize() {
	for d.rng < rcTop {
		d.rng <<= 8
		d.code = d.code<<8 | uint32(d.next())
	}
}

func (d *rcDecoder) bit(p *uint16) int {
	var b int
	d.rng, d.code, b = decStep(d.rng, d.code, p)
	if d.rng < rcTop {
		d.normalize()
	}
	return b
}

func (d *rcDecoder) tree(probs []uint16, n int) int {
	rng, code := d.rng, d.code
	m := 1
	for range n {
		var b int
		if rng, code, b = decStep(rng, code, &probs[m]); rng < rcTop {
			d.rng, d.code = rng, code
			d.normalize()
			rng, code = d.rng, d.code
		}
		m = m<<1 | b
	}
	d.rng, d.code = rng, code
	return m - 1<<n
}

// colour decodes a palette index against ref (-1: none), as rcCoder.colour
// describes.
func (d *rcDecoder) colour(pred *[3][256]uint16, ref int) int {
	rng, code := d.rng, d.code
	m, on := 1, ref >= 0
	for i := 7; i >= 0; i-- {
		s := 0
		if on {
			s = 1 + ref>>i&1
		}
		var b int
		if rng, code, b = decStep(rng, code, &pred[s][m]); rng < rcTop {
			d.rng, d.code = rng, code
			d.normalize()
			rng, code = d.rng, d.code
		}
		if on && b != ref>>i&1 {
			on = false
		}
		m = m<<1 | b
	}
	d.rng, d.code = rng, code
	return m - 256
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

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
