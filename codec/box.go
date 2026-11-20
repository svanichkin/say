package codec

// BoxScale: a frame larger than the cells' pixels is averaged down to them
// (each pixel the mean of the camera's pixels it covers) instead of taking
// one camera pixel for each.
var BoxScale = true

// boxScale averages the part of the frame the cells take (scaled to
// scaledW x scaledH, the cells' w x h from xOffset, yOffset) down to w x h
// pixels, each the mean of the source pixels it covers. The rows of a
// pixel are summed first (boxRows, a byte at a time down the rows into 16
// bits: a pixel covers far fewer than 257 rows), then its columns.
func (e *Encoder) boxScale(src []byte, srcW, w, h, scaledW, scaledH, srcH, xOffset, yOffset int) []byte {
	if cap(e.boxX) < w+1 {
		e.boxX = make([]int, w+1)
	}
	if cap(e.boxY) < h+1 {
		e.boxY = make([]int, h+1)
	}
	bx, by := e.boxX[:w+1], e.boxY[:h+1]
	// scaledW may come out a pixel short of w (float truncation): the last
	// pixels then still take a column of their own, as the nearest
	// sampling's clamp.
	for x := range w {
		bx[x] = min(srcW-1, (x+xOffset)*srcW/scaledW)
	}
	bx[w] = max(bx[w-1]+1, min(srcW, (w+xOffset)*srcW/scaledW))
	for y := range h {
		by[y] = min(srcH-1, (y+yOffset)*srcH/scaledH)
	}
	by[h] = max(by[h-1]+1, min(srcH, (h+yOffset)*srcH/scaledH))
	c0, c1 := bx[0]*3, bx[w]*3 // the bytes of a row the cells take
	if cap(e.box) < w*h*3 {
		e.box = make([]byte, w*h*3)
	}
	e.boxGen++ // the targets depend on the columns
	if cap(e.boxSum) < c1-c0 {
		e.boxSum = make([]uint16, c1-c0)
	}
	out, sum := e.box[:w*h*3], e.boxSum[:c1-c0]
	stride := srcW * 3
	for y := 0; y < h; y++ {
		y0, y1 := by[y], max(by[y+1], by[y]+1)
		boxRows(sum, src[y0*stride+c0:], stride, y1-y0)
		boxCols(out[y*w*3:], sum, e.boxTargets(bx, y1-y0))
	}
	return out
}

// boxTarget is a pixel of a row for boxCols: its sums' first byte's offset
// in the row (bytes), how many pixels it covers across, and the multiply
// and addend that divide its sum rounded by the pixels it covers in all.
type boxTarget struct {
	off, c, m, k uint32
}

// boxTargets is the row's pixels for a pixel ny rows high (kept for each
// height a frame has: two or three).
func (e *Encoder) boxTargets(bx []int, ny int) []boxTarget {
	w := len(bx) - 1
	for i := range e.boxTabs {
		if t := &e.boxTabs[i]; t.ny == ny && t.gen == e.boxGen {
			return t.tab
		}
	}
	t := &e.boxTabs[e.boxNext%len(e.boxTabs)]
	e.boxNext++
	t.ny, t.gen = ny, e.boxGen
	if cap(t.tab) < w {
		t.tab = make([]boxTarget, w)
	}
	t.tab = t.tab[:w]
	for x := range w {
		c := max(bx[x+1], bx[x]+1) - bx[x]
		// (a + n/2) / n as (a m + k) >> 24: exact, and below 2^32, for n
		// up to 266 (a is at most 255 n); checked for every a.
		n := uint32(c * ny)
		m := (1<<24 + n - 1) / n
		t.tab[x] = boxTarget{off: uint32((bx[x] - bx[0]) * 3), c: uint32(c), m: m, k: n / 2 * m}
	}
	return t.tab
}

// boxCols sets each pixel of a row to the mean of its sums (boxTarget).
func boxCols(out []byte, sum []uint16, tab []boxTarget) {
	if len(tab) == 0 {
		return
	}
	t := tab[len(tab)-1]
	_ = out[len(tab)*3-1]
	_ = sum[t.off+3*t.c-1]
	// In Go: a NEON loop (four lanes a pixel, widened, multiplied) took
	// longer, each pixel waiting on the one before.
	for x, t := range tab {
		var a0, a1, a2 uint32
		p := sum[t.off : t.off+3*t.c]
		switch len(p) {
		case 3:
			a0, a1, a2 = uint32(p[0]), uint32(p[1]), uint32(p[2])
		case 6:
			a0, a1, a2 = uint32(p[0])+uint32(p[3]), uint32(p[1])+uint32(p[4]), uint32(p[2])+uint32(p[5])
		case 9:
			a0, a1, a2 = uint32(p[0])+uint32(p[3])+uint32(p[6]), uint32(p[1])+uint32(p[4])+uint32(p[7]), uint32(p[2])+uint32(p[5])+uint32(p[8])
		case 12:
			a0 = uint32(p[0]) + uint32(p[3]) + uint32(p[6]) + uint32(p[9])
			a1 = uint32(p[1]) + uint32(p[4]) + uint32(p[7]) + uint32(p[10])
			a2 = uint32(p[2]) + uint32(p[5]) + uint32(p[8]) + uint32(p[11])
		default:
			for ; len(p) >= 3; p = p[3:] {
				a0 += uint32(p[0])
				a1 += uint32(p[1])
				a2 += uint32(p[2])
			}
		}
		o := out[x*3 : x*3+3 : x*3+3]
		o[0] = byte((a0*t.m + t.k) >> 24)
		o[1] = byte((a1*t.m + t.k) >> 24)
		o[2] = byte((a2*t.m + t.k) >> 24)
	}
}

// boxRows sets sum[i] to the sum of src[i], src[stride+i], ... over rows
// rows (rows >= 1, all of them in src).
func boxRows(sum []uint16, src []byte, stride, rows int) {
	n := len(sum)
	_ = src[(rows-1)*stride+n-1]
	done := boxRowsSIMD(sum, src, stride, rows) // a multiple of 16 bytes
	boxRowsGo(sum[done:], src[done:], stride, rows)
}

func boxRowsGo(sum []uint16, src []byte, stride, rows int) {
	for i := range sum {
		sum[i] = uint16(src[i])
	}
	for r := 1; r < rows; r++ {
		row := src[r*stride : r*stride+len(sum)]
		for i, v := range row {
			sum[i] += uint16(v)
		}
	}
}
