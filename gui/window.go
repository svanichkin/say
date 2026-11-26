//go:build darwin || windows

package gui

import (
	"errors"
	"github.com/svanichkin/say/keys"
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
// cells there are. Cmd+ and Cmd- change video detail one cell step at a time.
const (
	rowPoints = 16 // the height of a row of text, in points
	minCols   = 40
	minRows   = 16
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

	g := &game{screen: screen, in: in}
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
	minW, minH := int(float64(minCols*g.cw)/g.scale), int(float64(minRows*g.ch)/g.scale)
	ebiten.SetWindowTitle("Say")
	setWindowIcon()
	ebiten.SetWindowSize(w, h)
	g.place.path = in.Place
	if p, ok := loadPlace(in.Place); ok {
		// Where it was closed, as large (no smaller than the window can be).
		ebiten.SetWindowSize(max(p.W, minW), max(p.H, minH))
		ebiten.SetWindowPosition(p.X, p.Y)
		g.place.saved, g.place.seen = p, p
	}
	ebiten.SetWindowSizeLimits(minW, minH, -1, -1)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowClosingHandled(true)
	ebiten.SetRunnableOnUnfocused(true)
	ebiten.SetScreenClearedEveryFrame(false)
	catchQuit() // runs once the window's run loop does: its menu is there by then
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
	place     placeKeeper // the window's place, kept for the next start

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
	hovered        [2]int  // the cell under the pointer (hover)
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
		g.place.flush()
		if quitting() {
			g.closing = true
			g.closingAt = time.Now()
			log.Printf("[gui] quitting")
			if g.in.Interrupt != nil {
				g.in.Interrupt() // Say hangs up and stops; the window goes with it
			}
		} else {
			// The window's close button closes the window, not Say: it
			// goes on, waiting for calls (one coming in shows the window
			// again, so does its Dock icon); Cmd+Q, Ctrl+C quit.
			log.Printf("[gui] the window closed: Say goes on")
			HideWindow()
		}
	}
	if g.closing && time.Since(g.closingAt) > closeWait {
		// Say did not stop in time: the window goes anyway, and with it the
		// process (Run returns).
		log.Printf("[gui] Say did not stop within %v of closing the window: quitting", closeWait)
		return ebiten.Termination
	}
	if !g.closing {
		g.place.update(time.Now())
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
	if (ctrl && inpututil.IsKeyJustPressed(ebiten.KeyC)) || (meta && inpututil.IsKeyJustPressed(ebiten.KeyQ)) {
		if g.in.Interrupt != nil {
			g.in.Interrupt() // Ctrl+C, Cmd+Q: Say quits
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
		if g.in.Detail != nil {
			g.in.Detail(1)
		}
		return
	}
	if cmd && (inpututil.IsKeyJustPressed(ebiten.KeyMinus) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadSubtract)) {
		if g.in.Detail != nil {
			g.in.Detail(-1)
		}
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
	for _, k := range arrowKeys {
		if repeated(k.key) {
			g.in.Key(k.r)
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		g.in.Key(keys.Esc)
	}
}

// arrowKeys are the arrows as the interface's keys.
var arrowKeys = []struct {
	key ebiten.Key
	r   rune
}{{ebiten.KeyArrowUp, keys.Up}, {ebiten.KeyArrowDown, keys.Down}, {ebiten.KeyArrowLeft, keys.Left}, {ebiten.KeyArrowRight, keys.Right}}

// repeated reports a key press and its auto-repeat while held.
func repeated(k ebiten.Key) bool {
	d := inpututil.KeyPressDuration(k)
	return d == 1 || (d >= 30 && (d-30)%3 == 0)
}

func (g *game) mouse() {
	g.hover()
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

// hover tells where the pointer is when it moves to another cell (0, 0:
// out of the window).
func (g *game) hover() {
	if g.in.Hover == nil {
		return
	}
	x, y := ebiten.CursorPosition()
	cols, rows := g.screen.Size()
	at := [2]int{}
	if x >= g.ox && y >= g.oy {
		if col, row := (x-g.ox)/g.cw, (y-g.oy)/g.ch; col < cols && row < rows {
			at = [2]int{col + 1, row + 1}
		}
	}
	if at != g.hovered {
		g.hovered = at
		g.in.Hover(at[0], at[1])
	}
}

// quitting reports whether closing the window is quitting Say: Quit in the
// Dock or the app menu, Cmd+Q (Alt+F4 on Windows closes the window, as its
// button does).
func quitting() bool {
	return quitAsked() || ebiten.IsKeyPressed(ebiten.KeyMeta) && ebiten.IsKeyPressed(ebiten.KeyQ)
}

// HideWindow takes the window off the screen, Say going on, waiting for
// calls (macOS: the app hidden, its Dock icon brings it back; Windows: to
// the taskbar).
func HideWindow() { hideApp() }

// ShowWindow brings the window back, in front (a call coming in).
func ShowWindow() { showApp() }

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
