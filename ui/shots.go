package ui

import (
	"image"
	"time"

	"github.com/svanichkin/say/codec"
)

// The documentation's screenshots (shots_test.go in package main): the
// screen as Say draws it, with pictures in place of the cameras.

// ShotSetup makes the frames' shapes the same from run to run (they come
// from the addresses and a salt drawn at each start).
func ShotSetup() { frameLaunchSalt = 1 }

// Shot is the screen as it is now, cols x rows cells: what the window or
// the terminal would be sent.
func Shot(cols, rows int) string {
	// All of it: the screen is drawn on a clean one, not over the last.
	lastDrawMu.Lock()
	lastDraw.w, lastDraw.h, lastDraw.orient = 0, 0, 0
	lastDrawMu.Unlock()
	paneCache.reset()
	setSelfTermSize(cols, rows)
	data, _ := screenData(cols, rows)
	return data
}

// ShotVideo shows self as our camera and peer as the peer's, at the sizes
// their panes take on a screen cols x rows: each cut to its pane's shape
// (its middle; nothing stretched), as a call's frames are, in cells for a
// terminal and in pixels as well for the window.
func ShotVideo(self, peer image.Image, cols, rows int) {
	slots, ok := calcViewportSlots(cols, rows)
	if !ok {
		return
	}
	sc, sr := detailSize(slots.selfSlot.cols, slots.selfSlot.rows)
	pc, pr := detailSize(slots.peerSlot.cols, slots.peerSlot.rows)
	if f := shotFrame(self, sc, sr); f != nil {
		localFrameMu.Lock()
		localFrame.frame = f
		localFrame.version++
		localFrame.updatedAt = time.Now()
		localFrameMu.Unlock()
	}
	if f := shotFrame(peer, pc, pr); f != nil {
		remoteFrameMu.Lock()
		remoteFrame.frame = f
		remoteFrame.version++
		remoteFrame.updatedAt = time.Now()
		remoteFrameMu.Unlock()
	}
	// Their rates: 30 frames a second, as a camera's.
	fpsMu.Lock()
	for _, k := range []string{localFrameType, remoteFrameType} {
		c, now := ensureFpsCounter(k), time.Now()
		c.lastTick, c.frames = now.Add(-time.Second), 29
		c.recordFrame(now)
	}
	fpsMu.Unlock()
}

// shotFrame is img as a frame of cols x rows cells: cut to their shape
// (4x8 pixels a cell), encoded and decoded as a call's, and in the window
// the picture itself beside.
func shotFrame(img image.Image, cols, rows int) *codec.TextFrame {
	if img == nil || cols <= 0 || rows <= 0 {
		return nil
	}
	w, h := cols*4, rows*8
	ycc := make([]byte, 3*w*h)
	b := img.Bounds()
	// The middle of img in the pane's shape, sampled to w x h.
	cw, ch := b.Dx(), b.Dx()*h/w
	if ch > b.Dy() {
		cw, ch = b.Dy()*w/h, b.Dy()
	}
	x0, y0 := b.Min.X+(b.Dx()-cw)/2, b.Min.Y+(b.Dy()-ch)/2
	px := &codec.Pixels{W: w, H: h, Y: make([]byte, w*h), Cb: make([]byte, w*h), Cr: make([]byte, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(x0+x*cw/w, y0+y*ch/h).RGBA()
			Y, cb, cr := rgbToYCbCr(byte(r>>8), byte(g>>8), byte(bl>>8))
			i := y*w + x
			ycc[3*i], ycc[3*i+1], ycc[3*i+2] = Y, cb, cr
			px.Y[i], px.Cb[i], px.Cr[i] = Y, cb, cr
		}
	}
	data, err := codec.Encode(codec.Frame{Data: ycc, Width: w, Height: h}, cols, rows)
	if err != nil {
		return nil
	}
	f, err := codec.Decode(data)
	if err != nil {
		return nil
	}
	if pixelMode {
		f.Pixels = px
	}
	return f
}

func rgbToYCbCr(r, g, b byte) (byte, byte, byte) {
	R, G, B := int32(r), int32(g), int32(b)
	y := (19595*R + 38470*G + 7471*B + 1<<15) >> 16
	cb := (-11056*R - 21712*G + 32768*B + 257<<15) >> 16
	cr := (32768*R - 27440*G - 5328*B + 257<<15) >> 16
	clamp := func(v int32) byte {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return byte(v)
	}
	return clamp(y), clamp(cb), clamp(cr)
}
