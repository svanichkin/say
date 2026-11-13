//go:build darwin || windows

package gui

import (
	"errors"
	"log"
	"math"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/svanichkin/say/device"
)

// Available reports whether this system has the window.
func Available() bool { return true }

// The window works as a terminal does: the cell size follows the text
// (rowPoints tall, half as wide), and resizing the window changes how many
// cells there are. Cmd+ and Cmd- change the video detail instead: the same
// pane shows a frame of 2x or 4x the cells (44 -> 88 -> 176 wide).
const (
	rowPoints = 16 // the height of a row of text, in points
	minCols   = 40
	minRows   = 16
	maxDetail = 4
)

// closeWait is how long the window waits for Say to stop after its close
// button, before it closes anyway.
const closeWait = 5 * time.Second

// Room left on the monitor for the menu bar, the title bar and the dock.
const (
	marginW = 80
	marginH = 160
)

// Run shows the window and runs app meanwhile; the window closes when app
// returns. It must be called on the main goroutine (macOS wants windows on
// the main thread).
func Run(app func() error, in Input) error {
	screen := NewScreen(Cols, Rows)
	device.SetScreen(screen, screen.Size)
	keepAwake()

	g := &game{screen: screen, in: in, detail: 1}
	s := ebiten.Monitor().DeviceScaleFactor()
	g.setMetrics(s)

	var appErr error
	go func() {
		appErr = app()
		g.done.Store(true)
	}()

	mw, mh := ebiten.Monitor().Size()
	w := min(int(float64(Cols*g.cw)/g.scale), mw-marginW)
	h := min(int(float64(Rows*g.ch)/g.scale), mh-marginH)
	ebiten.SetWindowTitle("Say")
	ebiten.SetWindowSize(w, h)
	ebiten.SetWindowSizeLimits(int(float64(minCols*g.cw)/g.scale), int(float64(minRows*g.ch)/g.scale), -1, -1)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowClosingHandled(true)
	ebiten.SetRunnableOnUnfocused(true)
	ebiten.SetScreenClearedEveryFrame(false)
	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ebiten.Termination) {
		return err
	}
	return appErr
}

type game struct {
	screen    *Screen
	in        Input
	done      atomic.Bool
	closing   bool
	closingAt time.Time
	detail    int

	// Cell size and font, in device pixels, for the monitor's scale.
	scale  float64
	cw, ch int
	font   *fonts

	// Layout in device pixels: the window and where the grid starts in it.
	outW, outH int
	ox, oy     int

	// What was drawn last: redraw only when something changed.
	drawnAt        time.Time
	drawnVersion   uint64
	lastWinState   [2]bool // focused, minimized (DEBUG-TEMP)
	drawnW, drawnH int
	dirty          bool

	r renderer
}

// setMetrics sizes the cells and the font for a device scale factor. A cell
// is a multiple of 8x16 device pixels, so box drawing stays sharp and a
// Unifont pixel is a whole number of device pixels: rows come out nearest to
// rowPoints, the smaller size when two are as near (at 150%, say).
func (g *game) setMetrics(scale float64) {
	if scale <= 0 {
		scale = 1
	}
	if scale == g.scale && g.font != nil {
		return
	}
	g.scale = scale
	k := 2 * max(1, int(math.Ceil(rowPoints*scale/(2*cellH)-0.5)))
	g.cw, g.ch = cellW*k, cellH*k
	g.font = newFonts(float64(g.cw))
	g.r.shapes = map[uint32]*ebiten.Image{}
	g.dirty = true
	if g.in.Resized != nil {
		g.in.Resized() // text widths changed
	}
}

func (g *game) Update() error {
	if g.done.Load() {
		return ebiten.Termination
	}
	if ebiten.IsWindowBeingClosed() && !g.closing {
		g.closing = true
		g.closingAt = time.Now()
		log.Printf("[gui] the window is being closed")
		if g.in.Interrupt != nil {
			g.in.Interrupt() // Say hangs up and stops; the window goes with it
		}
	}
	if g.closing && time.Since(g.closingAt) > closeWait {
		// Say did not stop in time: the window goes anyway, and with it the
		// process (Run returns).
		log.Printf("[gui] Say did not stop within %v of closing the window: quitting", closeWait)
		return ebiten.Termination
	}
	g.keys()
	g.mouse()
	// DEBUG-TEMP: the window hidden or in the background can look like
	// video gone.
	if st := [2]bool{ebiten.IsFocused(), ebiten.IsWindowMinimized()}; st != g.lastWinState {
		log.Printf("[debug] window: focused %v, minimized %v", st[0], st[1])
		g.lastWinState = st
	}
	return nil
}

func (g *game) keys() {
	ctrl := ebiten.IsKeyPressed(ebiten.KeyControl)
	meta := ebiten.IsKeyPressed(ebiten.KeyMeta)
	cmd := meta || (runtime.GOOS != "darwin" && ctrl)
	if ctrl && inpututil.IsKeyJustPressed(ebiten.KeyC) {
		if g.in.Interrupt != nil {
			g.in.Interrupt()
		}
		return
	}
	if cmd && inpututil.IsKeyJustPressed(ebiten.KeyV) {
		if s := readClipboard(); s != "" && g.in.Paste != nil {
			g.in.Paste(s)
		}
		return
	}
	if cmd && (inpututil.IsKeyJustPressed(ebiten.KeyEqual) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadAdd)) {
		g.setDetail(g.detail * 2)
		return
	}
	if cmd && (inpututil.IsKeyJustPressed(ebiten.KeyMinus) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadSubtract)) {
		g.setDetail(g.detail / 2)
		return
	}
	if g.in.Key == nil {
		return
	}
	if !ctrl && !meta {
		for _, r := range ebiten.AppendInputChars(nil) {
			g.in.Key(r)
		}
	}
	if repeated(ebiten.KeyEnter) || repeated(ebiten.KeyNumpadEnter) {
		g.in.Key('\r')
	}
	if repeated(ebiten.KeyBackspace) {
		g.in.Key(0x7f)
	}
}

// setDetail changes how many cells of picture a pane holds per cell of the
// screen (1, 2 or 4); the peer is asked for frames that size.
func (g *game) setDetail(d int) {
	d = max(1, min(maxDetail, d))
	if d == g.detail {
		return
	}
	g.detail = d
	if g.in.Detail != nil {
		g.in.Detail(d)
	}
}

// repeated reports a key press and its auto-repeat while held.
func repeated(k ebiten.Key) bool {
	d := inpututil.KeyPressDuration(k)
	return d == 1 || (d >= 30 && (d-30)%3 == 0)
}

func (g *game) mouse() {
	if !inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) || g.in.Click == nil {
		return
	}
	x, y := ebiten.CursorPosition()
	if x < g.ox || y < g.oy {
		return
	}
	col, row := (x-g.ox)/g.cw, (y-g.oy)/g.ch
	cols, rows := g.screen.Size()
	if col >= cols || row >= rows {
		return
	}
	g.in.Click(col+1, row+1)
}

// Layout makes the screen the window's size in device pixels and fits the
// cell grid into it.
func (g *game) Layout(w, h int) (int, int) {
	s := ebiten.Monitor().DeviceScaleFactor()
	g.setMetrics(s)
	dw, dh := int(float64(w)*s), int(float64(h)*s)
	if dw != g.outW || dh != g.outH || g.dirty {
		if dw != g.outW || dh != g.outH { // DEBUG-TEMP
			log.Printf("[debug] window: %dx%d px", dw, dh)
		}
		g.outW, g.outH = dw, dh
		cols := max(minCols, dw/g.cw)
		// One row more than fits, half of it past the top and half past the
		// bottom: the margins there are rows of blank, and whole rows left
		// more room above and below than the side bars do at the sides.
		rows := max(minRows, dh/g.ch+1)
		if g.screen.Resize(cols, rows) && g.in.Resized != nil {
			g.in.Resized()
		}
		g.ox = max(0, (dw-cols*g.cw)/2)
		g.oy = (dh - rows*g.ch) / 2
		g.dirty = true
	}
	return dw, dh
}

func readClipboard() string {
	return device.ReadClipboard()
}
