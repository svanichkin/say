package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
	"golang.org/x/term"
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

		fmt.Fprint(os.Stdout, "\x1b[?2004h")
		go func() {
			<-ctx.Done()
			disableMouseReporting()
			fmt.Fprint(os.Stdout, "\x1b[?2004l")
			if restore != nil {
				restore()
			}
		}()

		go mouseEventLoop()
	})
}

func mouseEventLoop() {
	// The loop ends only when the terminal can no longer be read: it is
	// gone. Where closing it sends no hangup (Plan 9: vt exits), Say would
	// keep running unseen, holding its ports; stop it instead.
	defer func() {
		if exitOnInputLoss {
			interruptSelf()
		}
	}()
	reader := bufio.NewReader(os.Stdin)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return
		}
		if isInterruptKey(b) {
			interruptSelf()
			continue
		}
		if b != 0x1b { // ESC
			if b < utf8.RuneSelf {
				chatKey(rune(b))
			} else {
				if err := reader.UnreadByte(); err != nil {
					continue
				}
				r, _, err := reader.ReadRune()
				if err == nil {
					chatKey(r)
				}
			}
			continue
		}
		a, err := reader.ReadByte()
		if err != nil || a != '[' {
			continue
		}
		kind, err := reader.ReadByte()
		if err != nil {
			return
		}
		switch kind {
		case '<':
			if evt, ok := parseSGRMouseTail(reader); ok {
				handleMouseEvent(evt)
			}
		case '3': // ESC[3~, the Delete key (vt(1) -x sends it for Del)
			if c, e := reader.ReadByte(); e == nil && c == '~' && deleteKeyInterrupts {
				interruptSelf()
			}
		case '2':
			seq := []byte{kind}
			for len(seq) < 4 {
				c, e := reader.ReadByte()
				if e != nil {
					return
				}
				seq = append(seq, c)
			}
			if string(seq) == "200~" {
				var paste strings.Builder
				window := make([]byte, 0, 6)
				for {
					c, e := reader.ReadByte()
					if e != nil {
						return
					}
					window = append(window, c)
					if len(window) > 6 {
						paste.WriteByte(window[0])
						window = window[1:]
					}
					if string(window) == "\x1b[201~" {
						break
					}
				}
				chatPaste(paste.String())
			}
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
	return parseSGRMouseTail(r)
}

func parseSGRMouseTail(r *bufio.Reader) (mouseEvent, bool) {
	var evt mouseEvent
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
	canvasTop := termBorder + 1

	if col < termBorder+1 || col >= termBorder+1+canvasCols || row < canvasTop || row >= canvasTop+canvasRows {
		return viewportNone
	}

	local, _, _ := localFrameSnapshot()
	remote, _, _ := remoteFrameSnapshot()

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
	if row >= canvasTop+slots.selfSlot.rows {
		return viewportNone
	}
	groupCols := slots.selfSlot.cols + termSeparator + slots.peerSlot.cols
	videoLeft := termBorder + 1 + max(0, (ts.Cols-2*termBorder-groupCols)/2)
	if slots.selfSlot.cols <= 0 || slots.peerSlot.cols <= 0 {
		return viewportNone
	}
	if col >= videoLeft && col < videoLeft+slots.selfSlot.cols {
		if local != nil {
			return viewportSelf
		}
		return viewportNone
	}
	peerLeft := videoLeft + slots.selfSlot.cols + termSeparator
	if col >= peerLeft && col < peerLeft+slots.peerSlot.cols {
		if remote != nil {
			return viewportPeer
		}
	}
	return viewportNone
}

// interruptSelf stops Say as an interrupt from the terminal would: the raw
// mode some systems need for keys also turns off the terminal's own interrupt.
func interruptSelf() {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		_ = p.Signal(os.Interrupt)
	}
}
