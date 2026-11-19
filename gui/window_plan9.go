//go:build plan9

package gui

import (
	"bufio"
	_ "embed"
	"image"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/svanichkin/say/device"
)

// Plan 9: Say in a rio window of its own (not in vt), drawn through
// /dev/draw by the CPU, as the window elsewhere looks: the cells
// (softRenderer, Unifont's 8x16), the video panes as pictures in their HUD
// frames, the frames, the border and the captions (softPixels). Keys come as
// in a terminal (/dev/cons, raw); clicks from /dev/mouse; a resize
// re-attaches the window.

//go:embed fonts/unifont.otf.zst
var p9Unifont []byte

//go:embed fonts/unifont_upper.otf.zst
var p9UnifontUpper []byte

const p9CellW, p9CellH = 8, 16

// Available reports whether Say can draw a window here: under rio (or
// drawterm), with /dev/draw.
func Available() bool {
	for _, f := range []string{"/dev/winname", "/dev/draw/new", "/dev/mouse"} {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}
	return true
}

type p9Mouse struct {
	x, y, buttons int
	resize        bool
}

// Run shows the window and runs app meanwhile; it returns when app does.
func Run(app func() error, in Input) error {
	d, err := openDraw()
	if err != nil {
		return err
	}
	defer d.close()
	_ = os.WriteFile("/dev/label", []byte("Say"), 0)
	font, err := newSoftFont(p9CellH, p9Unifont, p9UnifontUpper)
	if err != nil {
		return err
	}
	r := newSoftRenderer(font, p9CellW, p9CellH)
	px := newSoftPixels(in, font, p9CellW, p9CellH)
	win, err := d.attach()
	if err != nil {
		return err
	}
	fit := func(win image.Rectangle) (int, int) {
		return max(1, win.Dx()/p9CellW), max(1, win.Dy()/p9CellH)
	}
	screen := NewScreen(fit(win))
	device.SetScreen(screen, screen.Size)
	quiet()

	done := make(chan error, 1)
	go func() { done <- app() }()
	mouse := make(chan p9Mouse, 16)
	go readMouse(mouse)
	stopKeys := readKeys(in)
	defer stopKeys()
	tick := time.NewTicker(time.Second / 30)
	defer tick.Stop()

	origin := func() image.Point { // the grid centred in the window
		cols, rows := screen.Size()
		return win.Min.Add(image.Pt((win.Dx()-cols*p9CellW)/2, (win.Dy()-rows*p9CellH)/2))
	}
	pressed := 0
	for {
		select {
		case err := <-done:
			return err
		case m := <-mouse:
			if m.resize {
				if win, err = d.attach(); err != nil {
					return err
				}
				if screen.Resize(fit(win)) && in.Resized != nil {
					in.Resized()
				}
				r.valid = false // the window was cleared: all of it again
				px.key = ""
				break
			}
			if m.buttons&1 != 0 && pressed&1 == 0 && in.Click != nil {
				o := origin()
				col, row := (m.x-o.X)/p9CellW, (m.y-o.Y)/p9CellH
				if cols, rows := screen.Size(); m.x >= o.X && m.y >= o.Y && col < cols && row < rows {
					in.Click(col+1, row+1)
				}
			}
			pressed = m.buttons
			continue
		case <-tick.C:
		}
		var rects []image.Rectangle
		brackets := screen.Brackets() // not under View: both take the screen's lock
		screen.View(func(cols, rows int, cells []Cell, panes []Pane, _ uint64) {
			// The pictures change without the cells: every tick.
			base := r.draw(cols, rows, cells)
			rects = px.update(r, panes, brackets, base)
		})
		if len(rects) == 0 {
			continue
		}
		d.buffer(r.w, r.h)
		o := origin()
		for _, rect := range rects {
			if err := d.load(rect, px.rowsOf(rect)); err != nil {
				return err
			}
			d.drawRect(winID, bufID, rect.Add(o), rect.Min)
		}
		if err := d.flush(); err != nil {
			return err
		}
	}
}

// readMouse sends what /dev/mouse says: m x y buttons msec, or r after the
// window was resized or moved.
func readMouse(out chan<- p9Mouse) {
	f, err := os.Open("/dev/mouse")
	if err != nil {
		return
	}
	defer f.Close()
	buf := make([]byte, 1+4*12)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		if n < len(buf) {
			continue
		}
		field := func(i int) int {
			v, _ := strconv.Atoi(strings.TrimSpace(string(buf[1+i*12 : 1+i*12+12])))
			return v
		}
		switch buf[0] {
		case 'r':
			out <- p9Mouse{resize: true}
		case 'm':
			out <- p9Mouse{x: field(0), y: field(1), buttons: field(2)}
		}
	}
}

// readKeys passes the keyboard to the interface: /dev/cons in raw mode
// (rawon, held while /dev/consctl stays open), a character at a time. Enter
// and Backspace as the window elsewhere sends them; Del, Plan 9's interrupt,
// and Ctrl-C stop Say. It returns what turns raw mode off.
func readKeys(in Input) func() {
	ctl, err := os.OpenFile("/dev/consctl", os.O_WRONLY, 0)
	if err != nil {
		return func() {}
	}
	if _, err := ctl.Write([]byte("rawon")); err != nil {
		ctl.Close()
		return func() {}
	}
	go func() {
		r := bufio.NewReader(os.Stdin)
		for {
			c, _, err := r.ReadRune()
			if err != nil {
				return
			}
			switch {
			case c == 0x7f || c == 0x03: // Del, Ctrl-C
				if in.Interrupt != nil {
					in.Interrupt()
				}
			case c == '\n' || c == '\r':
				if in.Key != nil {
					in.Key('\r')
				}
			case c == 0x08: // Backspace
				if in.Key != nil {
					in.Key(0x7f)
				}
			case c >= 0x20 && (c < 0xe000 || c > 0xf8ff): // not Plan 9's arrows and the like
				if in.Key != nil {
					in.Key(c)
				}
			}
		}
	}()
	return func() {
		_, _ = ctl.Write([]byte("rawoff"))
		ctl.Close()
	}
}

// quiet sends what Say prints to standard output and error to /dev/null:
// written to the window's console, rio would draw it over the picture. The
// log goes to its file (-log) as before.
func quiet() {
	null, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = syscall.Dup(int(null.Fd()), 1)
	_, _ = syscall.Dup(int(null.Fd()), 2)
}
