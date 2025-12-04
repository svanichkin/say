package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
	"say/logs"
	"say/mediactrl"
)

type mouseEvent struct {
	button  int
	col     int
	row     int
	pressed bool
}

var mouseTrackerOnce sync.Once

func startMouseTracker(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	mouseTrackerOnce.Do(func() {
		stdinFD := int(os.Stdin.Fd())
		stdoutFD := int(os.Stdout.Fd())
		if !term.IsTerminal(stdinFD) || !term.IsTerminal(stdoutFD) {
			logs.LogV("[term] mouse tracker disabled: stdio is not a TTY")
			return
		}

		restore, err := prepareTTYForMouse(stdinFD)
		if err != nil {
			logs.LogV("[term] mouse tracker disabled: %v", err)
			return
		}

		enableMouseReporting()

		go func() {
			<-ctx.Done()
			disableMouseReporting()
			if restore != nil {
				restore()
			}
		}()

		go mouseEventLoop()
	})
}

func mouseEventLoop() {
	reader := bufio.NewReader(os.Stdin)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return
		}
		if b != 0x1b { // ESC
			continue
		}
		if evt, ok := parseSGRMouse(reader); ok {
			handleMouseEvent(evt)
		}
	}
}

func parseSGRMouse(r *bufio.Reader) (mouseEvent, bool) {
	var evt mouseEvent
	next, err := r.ReadByte()
	if err != nil || next != '[' {
		return evt, false
	}
	next, err = r.ReadByte()
	if err != nil || next != '<' {
		return evt, false
	}
	btn, sep, err := readMouseComponent(r)
	if err != nil || sep != ';' {
		return evt, false
	}
	col, sep, err := readMouseComponent(r)
	if err != nil || sep != ';' {
		return evt, false
	}
	row, final, err := readMouseComponent(r)
	if err != nil {
		return evt, false
	}
	evt.button = btn
	evt.col = col
	evt.row = row
	evt.pressed = final == 'M'
	return evt, true
}

func readMouseComponent(r *bufio.Reader) (int, byte, error) {
	val := 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		if b >= '0' && b <= '9' {
			val = val*10 + int(b-'0')
			continue
		}
		return val, b, nil
	}
}

func handleMouseEvent(evt mouseEvent) {
	if !evt.pressed {
		return
	}
	if evt.button&0x03 != 0 {
		return
	}

	if statusClickHandlePoint(evt.col, evt.row) {
		return
	}

	switch viewportForPoint(evt.col, evt.row) {
	case viewportSelf:
		enabled := mediactrl.ToggleCapture()
		if enabled {
			logs.LogV("[ui] local capture resumed via click")
		} else {
			logs.LogV("[ui] local capture paused via click")
		}
	case viewportPeer:
		enabled := mediactrl.TogglePlayback()
		if enabled {
			logs.LogV("[ui] remote playback resumed via click")
		} else {
			logs.LogV("[ui] remote playback paused via click")
		}
	default:
		return
	}
	RequestRedraw()
}

func enableMouseReporting() {
	fmt.Fprint(os.Stdout, "\x1b[?1000h\x1b[?1006h")
}

func disableMouseReporting() {
	fmt.Fprint(os.Stdout, "\x1b[?1000l\x1b[?1006l")
}

type viewportID int

const (
	viewportNone viewportID = iota
	viewportSelf
	viewportPeer
)

func viewportForPoint(col, row int) viewportID {
	ts, ok := termSizeSnapshot()
	if !ok {
		return viewportNone
	}
	canvasCols := ts.Cols - 2*termBorder
	canvasRows := ts.Rows - 2*termBorder
	if canvasCols <= 0 || canvasRows <= 0 {
		return viewportNone
	}
	canvasLeft := termBorder + 1
	canvasTop := termBorder + 1

	if col < canvasLeft || col >= canvasLeft+canvasCols || row < canvasTop || row >= canvasTop+canvasRows {
		return viewportNone
	}

	local, _, _ := localFrameSnapshot()
	remote, _, _ := remoteFrameSnapshot()

	switch {
	case local == nil && remote == nil:
		return viewportNone
	case remote == nil:
		return viewportSelf
	case local == nil:
		return viewportPeer
	}

	slots, ok := calcViewportSlots(ts.Cols, ts.Rows)
	if !ok {
		return viewportNone
	}

	if slots.orient == 'V' {
		slotHeight := slots.selfSlot.rows
		if slotHeight <= 0 {
			return viewportNone
		}
		if row < canvasTop+slotHeight {
			return viewportSelf
		}
		return viewportPeer
	}

	slotWidth := slots.selfSlot.cols
	if slotWidth <= 0 {
		return viewportNone
	}
	if col < canvasLeft+slotWidth {
		return viewportSelf
	}
	return viewportPeer
}
