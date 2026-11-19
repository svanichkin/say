//go:build plan9

package gui

import (
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
)

// drawConn speaks draw(3), the protocol of /dev/draw, as libdraw does: the
// window rio gave us is attached by its name (/dev/winname); our pixels go
// into an image of ours ('y' messages), which is drawn onto the window ('d')
// where it changed, and flushed ('v').
type drawConn struct {
	ctl, data *os.File
	buf       []byte
	win       image.Rectangle // the window's inside (rio's border left out)
	attached  bool
	bufSize   image.Point // our image's size
}

const (
	drawMax = 8000 // a write to the data file; libdraw's size when iounit is unknown

	winID    = 1 // the window, named
	maskID   = 2 // opaque: draws through
	bufID    = 3 // our pixels
	blackID  = 4 // the margins
	rioInset = 4 // rio's window border, as libdraw's Borderwidth

	chanGrey1 = 0x31     // k1
	chanRGB24 = 0x081828 // r8g8b8
)

// openDraw connects to /dev/draw.
func openDraw() (*drawConn, error) {
	ctl, err := os.OpenFile("/dev/draw/new", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	info := make([]byte, 12*12)
	if _, err := ctl.Read(info); err != nil {
		ctl.Close()
		return nil, err
	}
	id, err := strconv.Atoi(strings.TrimSpace(string(info[:12])))
	if err != nil {
		ctl.Close()
		return nil, fmt.Errorf("draw: client id %q", info[:12])
	}
	data, err := os.OpenFile(fmt.Sprintf("/dev/draw/%d/data", id), os.O_RDWR, 0)
	if err != nil {
		ctl.Close()
		return nil, err
	}
	d := &drawConn{ctl: ctl, data: data}
	// An opaque mask and a black to clear with: 1x1, replicated.
	d.alloc(maskID, chanGrey1, true, image.Rect(0, 0, 1, 1), 0xFFFFFFFF)
	d.alloc(blackID, chanRGB24, true, image.Rect(0, 0, 1, 1), 0x000000FF)
	return d, d.flush()
}

func (d *drawConn) close() {
	d.data.Close()
	d.ctl.Close()
}

func (d *drawConn) long(v int) { d.buf = binary.LittleEndian.AppendUint32(d.buf, uint32(int32(v))) }

func (d *drawConn) rect(r image.Rectangle) {
	d.long(r.Min.X)
	d.long(r.Min.Y)
	d.long(r.Max.X)
	d.long(r.Max.Y)
}

func (d *drawConn) point(p image.Point) {
	d.long(p.X)
	d.long(p.Y)
}

// room makes room for a message of n bytes, sending what is buffered.
func (d *drawConn) room(n int) error {
	if len(d.buf)+n > drawMax {
		return d.write()
	}
	return nil
}

func (d *drawConn) write() error {
	if len(d.buf) == 0 {
		return nil
	}
	_, err := d.data.Write(d.buf)
	d.buf = d.buf[:0]
	return err
}

// flush sends what is buffered and asks the screen to show it.
func (d *drawConn) flush() error {
	if err := d.room(1); err != nil {
		return err
	}
	d.buf = append(d.buf, 'v')
	return d.write()
}

// alloc allocates image id: b id screenid refresh chan repl R clipR colour.
func (d *drawConn) alloc(id, ch int, repl bool, r image.Rectangle, rgba uint32) {
	d.room(51)
	d.buf = append(d.buf, 'b')
	d.long(id)
	d.long(0)
	d.buf = append(d.buf, 0)
	d.long(ch)
	clip := r
	if repl {
		d.buf = append(d.buf, 1)
		clip = image.Rect(-0x3FFFFFFF, -0x3FFFFFFF, 0x3FFFFFFF, 0x3FFFFFFF)
	} else {
		d.buf = append(d.buf, 0)
	}
	d.rect(r)
	d.rect(clip)
	d.buf = binary.LittleEndian.AppendUint32(d.buf, rgba)
}

func (d *drawConn) free(id int) {
	d.room(5)
	d.buf = append(d.buf, 'f')
	d.long(id)
}

// attach names rio's window image (again after a resize: its name changes)
// and returns its inside.
func (d *drawConn) attach() (image.Rectangle, error) {
	name, err := os.ReadFile("/dev/winname")
	if err != nil {
		return image.Rectangle{}, err
	}
	if d.attached {
		d.free(winID)
	}
	d.room(6 + len(name))
	d.buf = append(d.buf, 'n')
	d.long(winID)
	d.buf = append(d.buf, byte(len(name)))
	d.buf = append(d.buf, name...)
	if err := d.write(); err != nil {
		return image.Rectangle{}, fmt.Errorf("draw: window %q: %w", name, err)
	}
	d.attached = true
	info := make([]byte, 12*12)
	if _, err := d.ctl.ReadAt(info, 0); err != nil {
		return image.Rectangle{}, err
	}
	field := func(i int) int {
		v, _ := strconv.Atoi(strings.TrimSpace(string(info[i*12 : i*12+12])))
		return v
	}
	r := image.Rect(field(4), field(5), field(6), field(7))
	d.win = r.Inset(rioInset)
	// The window black, under what we draw.
	d.drawRect(winID, blackID, d.win, image.Point{})
	return d.win, d.flush()
}

// buffer gives our image size w x h.
func (d *drawConn) buffer(w, h int) {
	if d.bufSize == (image.Point{w, h}) {
		return
	}
	if d.bufSize != (image.Point{}) {
		d.free(bufID)
	}
	d.alloc(bufID, chanRGB24, false, image.Rect(0, 0, w, h), 0x000000FF)
	d.bufSize = image.Point{w, h}
}

// load sends rect of our image: pix holds its rows, softBytes per pixel.
// A message holds whole rows; a row too long for one goes in pieces.
func (d *drawConn) load(rect image.Rectangle, pix []byte) error {
	rowBytes := rect.Dx() * softBytes
	const head = 1 + 4 + 16
	if rowBytes > drawMax-head {
		// Pieces of the width that fit.
		step := (drawMax - head) / softBytes
		for x := rect.Min.X; x < rect.Max.X; x += step {
			part := image.Rect(x, rect.Min.Y, min(x+step, rect.Max.X), rect.Max.Y)
			var sub []byte
			for y := 0; y < rect.Dy(); y++ {
				o := y*rowBytes + (x-rect.Min.X)*softBytes
				sub = append(sub, pix[o:o+part.Dx()*softBytes]...)
			}
			if err := d.load(part, sub); err != nil {
				return err
			}
		}
		return nil
	}
	rows := max(1, (drawMax-head)/rowBytes)
	for y := 0; y < rect.Dy(); y += rows {
		n := min(rows, rect.Dy()-y)
		if err := d.room(head + n*rowBytes); err != nil {
			return err
		}
		d.buf = append(d.buf, 'y')
		d.long(bufID)
		d.rect(image.Rect(rect.Min.X, rect.Min.Y+y, rect.Max.X, rect.Min.Y+y+n))
		d.buf = append(d.buf, pix[y*rowBytes:(y+n)*rowBytes]...)
	}
	return nil
}

// drawRect draws src onto dst at r, from src's point sp, through the opaque
// mask.
func (d *drawConn) drawRect(dst, src int, r image.Rectangle, sp image.Point) {
	d.room(45)
	d.buf = append(d.buf, 'd')
	d.long(dst)
	d.long(src)
	d.long(maskID)
	d.rect(r)
	d.point(sp)
	d.point(sp)
}
