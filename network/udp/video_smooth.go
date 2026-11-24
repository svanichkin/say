package udp

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/svanichkin/Babe/pkg/babe"
	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/ui"
)

// Video with Babe's split coder (the default; Say's cells with -video-codec
// say, both peers): each band a stream of pixels (babe.SplitVideoEncoder:
// blocks of an error-split tree that copy the frame before, moved or not,
// or are drawn anew) in place of Say's cells; the receiver turns the pixels into its
// cells itself (codec.Encoder.SelfView), so the panes, the noise of a band
// that hung, the recovery and the acknowledgements stay as they are (the
// frames' numbers are uint16 as Say's). In the window (ui.PixelMode) no
// cells are made: the frame carries the pixels (codec.TextFrame.Pixels) and
// the window draws them. The grid is the pane's (the cells
// no longer cost the stream); the rate is met by the quality instead, from
// videoSmoothQ down, steered on every change frame to the bytes a frame the
// rate allows, and by the frames the budget has no room for.
//
// A band's frame: videoSmoothMark, the cells' columns and rows the sender
// asks for, then the smooth frame.

// videoSmoothQ is the quality a path with trouble gets at most (loss, a
// queue, a cut of the rate: big frames break into IP fragments and queue);
// a path clean for smoothCleanFor lets it rise, a point every
// smoothQRise, up to videoSmoothQMax (q60 was some 4 times q40's bytes on
// a call, q50 about twice: 20 KB/s at 352x288 and 30 fps).
var (
	videoSmooth     = true
	videoSmoothQ    = 40
	videoSmoothQMax = 50
)

const (
	smoothCleanFor  = 3 * time.Second
	smoothQRise     = 500 * time.Millisecond
	smoothLogEvery  = 2 * time.Second
	smoothBadHold   = 30 * time.Second // a quality trouble came at is not reached again for this long ...
	smoothBadMargin = 5.0              // ... the allowed staying this far below it
	// The rate may stand this high while the picture is still (kbit/s).
	smoothRateFloor = 1000.0
	// Loss that counts as trouble: more than a datagram a second at 30
	// frames (one late at a window's edge looks like one lost).
	smoothTroubleLoss = 0.04
)

const (
	videoSmoothMark      byte = 0xB5
	videoSmoothQPerTwice      = 13  // quality points that about double a change frame
	videoSmoothFill           = 0.9 // of the band's share of the rate a frame is steered to
	videoSmoothQMin           = 10
)

var errSmoothFrame = errors.New("not a smooth video frame")

// peerPixels: the peer shows our video in pixels (its window): the frames
// keep the camera's pixels, cropped to its pane's shape, not shrunk to its
// cells.
var peerPixels atomic.Bool

// SetPeerPixels says whether the peer shows our video in pixels.
func SetPeerPixels(on bool) { peerPixels.Store(on) }

// smoothCropKeep: a crop is kept while the pane's shape stays within this
// share of it (the window's detail steps change the grid by a cell or two:
// a new size each time would be a keyframe each time).
const smoothCropKeep = 0.03

// SetVideoCodec picks the video coder for future sessions: "smooth" (the
// default) or "say" (Say's cells, as before; both peers); q the smooth coder's quality on a path
// with trouble, qmax on a clean one (0: the defaults).
func SetVideoCodec(name string, q, qmax int) error {
	switch name {
	case "", "smooth":
		videoSmooth = true
	case "say":
		videoSmooth = false
	default:
		return fmt.Errorf("unknown -video-codec %q: want say or smooth", name)
	}
	if q > 0 {
		videoSmoothQ = min(99, q)
	}
	if qmax > 0 {
		videoSmoothQMax = min(99, qmax)
	}
	videoSmoothQMax = max(videoSmoothQMax, videoSmoothQ)
	return nil
}

// newSmoothCoders are Babe's split encoder and decoder as Babe's
// cmd/babecam codes video (newSplitCoders): no screen layer of text cells
// on a camera (it took long stretches of a call, nothing copied under it).
func newSmoothCoders() (*babe.Encoder, *babe.Decoder) {
	e := babe.NewEncoder()
	d := babe.NewDecoder()
	e.Mode = babe.ModeSplit
	e.LumaSplit, e.ChromaSplit, e.JointSplit = true, true, true
	e.BTCLeaves, e.BTCFine, e.Fast, e.Blend = true, true, true, true
	e.NoScreen = true
	// One thread: a frame this small is coded in some 13 ms (352x288, q40)
	// at about 30% less CPU than its parallel workers spend (Babe's
	// cpucheck: 18 ms of CPU for 4 ms by the clock).
	e.Parallel, d.Parallel = false, false
	return e, d
}

// bandCoder codes a band's frames: Say's (sayBand), or smoothBand. The
// frames to code against are the receiver's, the first that is kept
// (Say's takes the first).
type bandCoder interface {
	ForceKeyframe()
	RecoverFrom(ids ...uint16)
	CodeAgainst(ids ...uint16)
	SetSkipEmpty(on bool)
	Encode(f codec.Frame, cols, rows int) ([]byte, error)
}

// sayBand is Say's encoder as a bandCoder.
type sayBand struct{ *codec.Encoder }

func (b sayBand) RecoverFrom(ids ...uint16) {
	if len(ids) > 0 {
		b.Encoder.RecoverFrom(ids[0])
	}
}

func (b sayBand) CodeAgainst(ids ...uint16) {
	if len(ids) > 0 {
		b.Encoder.CodeAgainst(ids[0])
	}
}

// bandDecoder decodes a band's frames into cells: Say's codec.Decoder, or
// smoothRx (which also keeps marks, frames kept for seconds: LastMark).
type bandDecoder interface {
	Decode(data []byte) (*codec.TextFrame, error)
	LastGood() (uint16, bool)
}

// The smooth sender's reactions to the path (as tried in Babe's
// cmd/babecam): a queue growing now cuts the rate at once
// (rateCtl.queueing; not within smoothAfterSilent of an outage, when a
// longer path looks the same); while a standing queue of smoothQueueHigh
// stands with less arriving than went, it sends at most smoothDrain of
// what arrives; when the reports stop for smoothSilent it codes against
// the frames the receiver has.
const (
	smoothQueueFast   = 100 * time.Millisecond
	smoothFastWin     = 3 * rateReportEvery
	smoothQueueHigh   = 150 * time.Millisecond
	smoothDrain       = 0.7
	smoothSilent      = 300 * time.Millisecond
	smoothAfterSilent = 5 * time.Second
)

// isKeyframe says whether a band's frame is a keyframe.
func isKeyframe(b []byte) bool {
	if len(b) > 3 && b[0] == videoSmoothMark {
		return b[3] == 'K'
	}
	return codec.IsKeyframe(b) // Say's
}

// smoothBand is a band's smooth encoder: its quality steered to the bytes a
// frame its share of the rate allows.
type smoothBand struct {
	enc      *babe.SplitVideoEncoder
	rate     func() float64 // kbit/s the sender may send at
	qCap     func() float64 // the quality the path allows now
	logAt    time.Duration
	logKeys  int
	logBytes int
	logN     int
	share    float64 // of the rate: the band's rows over the grid's
	q        float64
	interval float64 // seconds between frames (mean)
	now      time.Duration
	last     time.Duration
	frame    *image.YCbCr
}

func newSmoothBand(rate, qCap func() float64, share float64) *smoothBand {
	e, d := newSmoothCoders()
	return &smoothBand{enc: babe.NewSplitVideoEncoder(e, d), rate: rate, qCap: qCap, share: share, q: float64(videoSmoothQ)}
}

func (b *smoothBand) ForceKeyframe()            { b.enc.ForceKeyframe() }
func (b *smoothBand) RecoverFrom(ids ...uint16) { b.enc.RecoverFrom(ids...) }
func (b *smoothBand) CodeAgainst(ids ...uint16) { b.enc.CodeAgainst(ids...) }

// SetSkipEmpty: a frame the camera hardly changed from the one its
// reference stands for is not sent (babe's SkipUnchanged; the heartbeat
// still sends one).
func (b *smoothBand) SetSkipEmpty(on bool) { b.enc.SkipUnchanged = on }

// setNow is the sender's time of the frame Encode codes next.
func (b *smoothBand) setNow(now time.Duration) { b.now = now }

// Encode codes the band's pixels (packed Y Cb Cr) at the quality the rate
// allows; cols and rows go with it for the receiver's cells.
func (b *smoothBand) Encode(f codec.Frame, cols, rows int) ([]byte, error) {
	now := b.now
	if b.last > 0 {
		dt := min(0.5, (now - b.last).Seconds())
		if b.interval == 0 {
			b.interval = dt
		} else {
			b.interval = 0.9*b.interval + 0.1*dt
		}
	}
	b.last = now
	w, h := f.Width, f.Height
	if b.frame == nil || b.frame.Rect.Dx() != w || b.frame.Rect.Dy() != h {
		b.frame = image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio444)
	}
	for i := 0; i < w*h; i++ {
		b.frame.Y[i], b.frame.Cb[i], b.frame.Cr[i] = f.Data[3*i], f.Data[3*i+1], f.Data[3*i+2]
	}
	b.q = min(b.qCap(), b.q)
	data, _, _, _, err := b.enc.Encode(b.frame, int(math.Round(b.q)))
	if err != nil || len(data) == 0 {
		return nil, err // nothing changed (SetSkipEmpty)
	}
	if !b.enc.LastKey() {
		b.steer(len(data))
	} else {
		b.logKeys++
	}
	b.logBytes += len(data)
	b.logN++
	if now-b.logAt >= smoothLogEvery {
		log.Printf("[video/smooth] %dx%d (cells %dx%d) q%.0f of %.0f, %d frames, %d B a frame, %d keyframes, rate %.0f kbit/s",
			w, h, cols, rows, b.q, b.qCap(), b.logN, b.logBytes/max(1, b.logN), b.logKeys, b.rate())
		b.logAt, b.logBytes, b.logN, b.logKeys = now, 0, 0, 0
	}
	return append([]byte{videoSmoothMark, byte(min(255, cols)), byte(min(255, rows))}, data...), nil
}

// steer moves the quality toward the bytes a frame the band's share of the
// rate allows: a frame twice too large takes about videoSmoothQPerTwice
// points off, growing back slower.
func (b *smoothBand) steer(n int) {
	if b.interval <= 0 {
		return
	}
	target := videoSmoothFill * b.share * b.rate() * 1000 / 8 * b.interval
	step := 0.5 * videoSmoothQPerTwice * math.Log2(target/max(1, float64(n)))
	if step < 0 {
		step *= 2
	}
	c := b.qCap()
	b.q = min(c, max(min(videoSmoothQMin, c), b.q+min(1, max(-15, step))))
}

// pixelFrame is a frame of cols x rows cells that only holds the grid's
// size, the picture in pixels (the window draws Pixels).
func pixelFrame(cols, rows int, p *codec.Pixels) *codec.TextFrame {
	n := cols * rows
	buf := make([]byte, 7*n)
	return &codec.TextFrame{Cols: cols, Rows: rows, Width: cols << 2, Height: rows << 3,
		Glyphs: buf[:n:n], FgYCbCr: buf[n : 4*n : 4*n], BgYCbCr: buf[4*n:], Pixels: p}
}

// cropCover is the middle of f in the shape of a pane of cols x rows cells
// (each 4 x 8 pixels), as Say's codec fits the camera to the cells: what
// is outside is not shown, so it is not sent either. The result may share
// buf (grown as needed); f itself when its shape is the pane's.
func cropCover(f codec.Frame, cols, rows int, buf *[]byte) codec.Frame {
	nw, nh := cropSize(f.Width, f.Height, cols, rows)
	return cropTo(f, nw, nh, buf)
}

// cropSize is the size of the middle of a w x h frame in the shape of a
// pane of cols x rows cells: the larger side cropped, even.
func cropSize(w, h, cols, rows int) (int, int) {
	if cols <= 0 || rows <= 0 || w <= 0 || h <= 0 {
		return w, h
	}
	nw, nh := w, h
	if w*rows*8 > h*cols*4 {
		nw = max(2, (h*cols*4/(rows*8))&^1)
	} else {
		nh = max(2, (w*rows*8/(cols*4))&^1)
	}
	return min(nw, w), min(nh, h)
}

// cropTo is the middle nw x nh of f (f itself when it is that size).
func cropTo(f codec.Frame, nw, nh int, buf *[]byte) codec.Frame {
	w, h := f.Width, f.Height
	if nw >= w && nh >= h || nw <= 0 || nh <= 0 {
		return f
	}
	nw, nh = min(nw, w), min(nh, h)
	x0, y0 := (w-nw)/2, (h-nh)/2
	if cap(*buf) < 3*nw*nh {
		*buf = make([]byte, 3*nw*nh)
	}
	out := (*buf)[:3*nw*nh]
	for y := range nh {
		copy(out[3*nw*y:3*nw*(y+1)], f.Data[3*((y0+y)*w+x0):3*((y0+y)*w+x0+nw)])
	}
	return codec.Frame{Data: out, Width: nw, Height: nh}
}

// shrinkCover is f at most w x h pixels (the pane's cells, 4 x 8 each: what
// the peer can show), each pixel the mean of those it covers; f itself
// when it is no larger. f is in the pane's shape (cropCover).
func shrinkCover(f codec.Frame, w, h int, buf *[]byte) codec.Frame {
	sw, sh := f.Width, f.Height
	if w <= 0 || h <= 0 || sw <= w && sh <= h {
		return f
	}
	w, h = min(w, sw), min(h, sh)
	w, h = w&^1, h&^1
	if cap(*buf) < 3*w*h {
		*buf = make([]byte, 3*w*h)
	}
	out := (*buf)[:3*w*h]
	for y := range h {
		y0, y1 := y*sh/h, max(y*sh/h+1, (y+1)*sh/h)
		for x := range w {
			x0, x1 := x*sw/w, max(x*sw/w+1, (x+1)*sw/w)
			var s [3]int
			for yy := y0; yy < y1; yy++ {
				row := f.Data[3*(yy*sw+x0) : 3*(yy*sw+x1)]
				for i := 0; i < len(row); i += 3 {
					s[0], s[1], s[2] = s[0]+int(row[i]), s[1]+int(row[i+1]), s[2]+int(row[i+2])
				}
			}
			n := (y1 - y0) * (x1 - x0)
			o := 3 * (y*w + x)
			out[o], out[o+1], out[o+2] = byte((s[0]+n/2)/n), byte((s[1]+n/2)/n), byte((s[2]+n/2)/n)
		}
	}
	return codec.Frame{Data: out, Width: w, Height: h}
}

// pixelSelfView is the self view in pixels (smooth video in the window):
// the camera's frame in the pane's shape, mirrored left to right, as Say's
// self view.
func pixelSelfView(f codec.Frame, cols, rows int) *codec.TextFrame {
	var buf []byte
	f = cropCover(f, cols, rows, &buf)
	w, h := f.Width, f.Height
	p := &codec.Pixels{W: w, H: h, Y: make([]byte, w*h), Cb: make([]byte, w*h), Cr: make([]byte, w*h)}
	for y := range h {
		for x := range w {
			s, d := 3*(y*w+w-1-x), y*w+x
			p.Y[d], p.Cb[d], p.Cr[d] = f.Data[s], f.Data[s+1], f.Data[s+2]
		}
	}
	return pixelFrame(cols, rows, p)
}

// smoothRx is a band's smooth decoder: in the window the picture as Babe
// shows it (PictureNative: the RGB stages' blend), in the terminal into
// cells (codec.Encoder.SelfView, the receiver's own, no compression).
type smoothRx struct {
	dec   *babe.SplitVideoDecoder
	cells *codec.Encoder
	data  []byte
	pix   [3][]byte // the picture's planes when it came as RGBA
}

func newSmoothRx() *smoothRx {
	_, d := newSmoothCoders()
	return &smoothRx{dec: babe.NewSplitVideoDecoder(d), cells: codec.NewEncoder(false, 0, maxVideoFPS, false)}
}

func (r *smoothRx) LastGood() (uint16, bool) { return r.dec.LastGood() }
func (r *smoothRx) LastMark() (uint16, bool) { return r.dec.LastMark() }

func (r *smoothRx) Decode(data []byte) (*codec.TextFrame, error) {
	if len(data) < 4 || data[0] != videoSmoothMark || data[1] == 0 || data[2] == 0 {
		return nil, errSmoothFrame
	}
	cols, rows := int(data[1]), int(data[2])
	w, h, y, cb, cr, err := r.dec.Decode(data[3:])
	if err != nil {
		return nil, err
	}
	// The picture shown (as Babe's BabeCam shows it), not the reference.
	if y, cb, cr, err = r.shown(w, h, y, cb, cr); err != nil {
		return nil, err
	}
	if ui.PixelMode() {
		return pixelFrame(cols, rows, &codec.Pixels{W: w, H: h, Y: y, Cb: cb, Cr: cr}), nil
	}
	if cap(r.data) < 3*w*h {
		r.data = make([]byte, 3*w*h)
	}
	r.data = r.data[:3*w*h]
	for i := 0; i < w*h; i++ {
		r.data[3*i], r.data[3*i+1], r.data[3*i+2] = y[i], cb[i], cr[i]
	}
	return r.cells.SelfView(codec.Frame{Data: r.data, Width: w, Height: h}, cols, rows)
}

// shown is the planes of the picture the frame just decoded shows
// (babe.SplitVideoDecoder.PictureNative: 4:4:4 planes, or an RGBA turned
// into them by JFIF's formula); the decoded planes when neither.
func (r *smoothRx) shown(w, h int, y, cb, cr []byte) ([]byte, []byte, []byte, error) {
	im, err := r.dec.PictureNative()
	if err != nil {
		return nil, nil, nil, err
	}
	switch p := im.(type) {
	case *image.YCbCr:
		if p.SubsampleRatio == image.YCbCrSubsampleRatio444 && p.Rect.Dx() == w && p.Rect.Dy() == h && p.YStride == w && p.CStride == w {
			return p.Y[:w*h], p.Cb[:w*h], p.Cr[:w*h], nil
		}
	case *image.RGBA:
		if p.Rect.Dx() == w && p.Rect.Dy() == h {
			for c := range r.pix {
				if cap(r.pix[c]) < w*h {
					r.pix[c] = make([]byte, w*h)
				}
				r.pix[c] = r.pix[c][:w*h]
			}
			for py := range h {
				row := p.Pix[py*p.Stride:]
				for px := range w {
					o := 4 * px
					yy, u, v := color.RGBToYCbCr(row[o], row[o+1], row[o+2])
					i := py*w + px
					r.pix[0][i], r.pix[1][i], r.pix[2][i] = yy, u, v
				}
			}
			return r.pix[0], r.pix[1], r.pix[2], nil
		}
	}
	return y, cb, cr, nil
}
