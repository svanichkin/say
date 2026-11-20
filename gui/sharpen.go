package gui

import (
	"sync"
	"sync/atomic"
)

// The sharpness (-video-sharp, the window only): an unsharp mask over the
// picture's luma at the picture's own resolution, before it is drawn
// larger over the pane (drawn larger first, a pixel of it was a few of the
// screen's and the mask had nothing to find). Differences from the
// neighbourhood under sharpThreshold are left alone (the coder's noise and
// its flat blocks), the rest pushed apart by the amount, the overshoot held
// within sharpHalo of the neighbourhood's own range (no rings). The stream
// is not touched.
var videoSharp atomic.Int32 // the amount in percent (0: off)

func init() { videoSharp.Store(sharpDefault) }

// SetVideoSharp sets the sharpness amount in percent (0: off).
func SetVideoSharp(percent int) { videoSharp.Store(int32(max(0, percent))) }

const (
	sharpDefault   = 100
	sharpThreshold = 2  // luma levels
	sharpHalo      = 12 // luma levels past the neighbourhood's range
)

var sharpBufs = sync.Pool{New: func() any { return new([]byte) }}

// sharpened returns y (w x h) sharpened (see videoSharp), and a function
// giving its buffer back; y itself when the sharpness is off.
func sharpened(y []byte, w, h int) ([]byte, func()) {
	amount := int(videoSharp.Load())
	if amount <= 0 || w < 3 || h < 3 || len(y) < w*h {
		return y, func() {}
	}
	bp := sharpBufs.Get().(*[]byte)
	if cap(*bp) < w*h {
		*bp = make([]byte, w*h)
	}
	out := (*bp)[:w*h]
	sharpenY(out, y, w, h, amount)
	return out, func() { sharpBufs.Put(bp) }
}

// sharpenY writes src (w x h luma) sharpened by amount percent into dst.
func sharpenY(dst, src []byte, w, h, amount int) {
	copy(dst[:w], src[:w])
	copy(dst[(h-1)*w:w*h], src[(h-1)*w:w*h])
	for y := 1; y < h-1; y++ {
		a, b, c := src[(y-1)*w:y*w], src[y*w:(y+1)*w], src[(y+1)*w:(y+2)*w]
		o := dst[y*w : (y+1)*w]
		o[0], o[w-1] = b[0], b[w-1]
		for x := 1; x < w-1; x++ {
			// The neighbourhood: a 3x3 [1 2 1] blur, and its range.
			blur := (int(a[x-1]) + 2*int(a[x]) + int(a[x+1]) +
				2*int(b[x-1]) + 4*int(b[x]) + 2*int(b[x+1]) +
				int(c[x-1]) + 2*int(c[x]) + int(c[x+1]) + 8) >> 4
			v := int(b[x])
			d := v - blur
			switch {
			case d > sharpThreshold:
				d -= sharpThreshold
			case d < -sharpThreshold:
				d += sharpThreshold
			default:
				o[x] = b[x]
				continue
			}
			lo, hi := v, v
			for _, n := range [8]byte{a[x-1], a[x], a[x+1], b[x-1], b[x+1], c[x-1], c[x], c[x+1]} {
				lo, hi = min(lo, int(n)), max(hi, int(n))
			}
			s := v + d*amount/50 // the difference from a 3x3 blur is about half an edge's
			o[x] = byte(min(255, hi+sharpHalo, max(0, lo-sharpHalo, s)))
		}
	}
}
