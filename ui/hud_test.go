package ui

import (
	"strings"
	"testing"
	"time"
)

// In a terminal with 24-bit colour the call screen has the window's frames,
// drawn in characters: no marks meant for the window, a frame round each
// pane and the chat, the captions on a bracketed line, the side bars.
func TestTermHUDDrawsTheWindowsFrames(t *testing.T) {
	setTermHUD(true)
	defer setTermHUD(false)
	defer func() {
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	for _, size := range [][2]int{{120, 44}, {96, 45}, {80, 30}, {200, 60}} {
		cols, rows := size[0], size[1]
		paneCache = screenCache{}
		out := renderChatScreen(cols, rows)
		if strings.Contains(onBlack(out), "\x1b[0m") {
			t.Fatalf("%dx%d: a reset to the terminal's own colours", cols, rows)
		}
		if strings.Contains(out, "\x1b]") {
			t.Fatalf("%dx%d: marks for the window written to a terminal", cols, rows)
		}
		vs, ok := calcViewportSlots(cols, rows)
		if !ok {
			t.Fatalf("%dx%d: no layout", cols, rows)
		}
		v := newVterm(cols, rows)
		v.apply(out)
		at := func(row, col int) rune { return v.cells[(row-1)*cols+col-1].r }
		canvasLeft, canvasTop := termSide+1, termBorder+1
		groupCols := vs.selfSlot.cols + termGap + vs.peerSlot.cols
		videoLeft := canvasLeft + max(0, (cols-2*termSide-groupCols)/2)
		peerLeft := videoLeft + vs.selfSlot.cols + termGap
		for _, left := range []int{videoLeft, peerLeft} {
			top, bottom := canvasTop-1, canvasTop+vs.selfSlot.rows
			right := left + vs.selfSlot.cols
			// The corners are cut: a quarter joining the edges, or a
			// slant from further in.
			if !strings.ContainsRune("▗ ", at(top, left-1)) || !strings.ContainsRune("▖ ", at(top, right)) ||
				!strings.ContainsRune("▝ ", at(bottom, left-1)) || !strings.ContainsRune("▘ ", at(bottom, right)) {
				t.Fatalf("%dx%d: no cut corners on the pane at column %d", cols, rows, left)
			}
			mid := left + vs.selfSlot.cols/2
			if at(top, mid) == 0 || at(bottom, mid) == 0 || at(canvasTop+vs.selfSlot.rows/2, left-1) == 0 {
				t.Fatalf("%dx%d: no frame round the pane at column %d", cols, rows, left)
			}
		}
		captionRow := canvasTop + vs.selfSlot.rows + 1
		if at(captionRow, videoLeft) != '▐' || at(captionRow, videoLeft+groupCols-1) != '▌' || at(captionRow, videoLeft+1) != '─' {
			t.Fatalf("%dx%d: the captions' row is not bracketed", cols, rows)
		}
		for r := 1; r <= rows; r++ {
			if at(r, 1) == 0 || at(r, cols) == 0 {
				t.Fatalf("%dx%d: row %d has no side bars", cols, rows, r)
			}
		}
	}
}

// Without 24-bit colour (Plan 9's vt, -colors 16) the terminal keeps its
// plain layout.
func TestTermHUDNeedsTrueColour(t *testing.T) {
	was := ColorMode(colorMode.Load())
	defer func() { colorMode.Store(int32(was)); setTermHUD(false) }()
	SetColorMode("16")
	if termHUD || termBorder != 1 {
		t.Fatal("frames drawn with 16 colours")
	}
	SetColorMode("truecolor")
	if !termHUD || termBorder != 2 {
		t.Fatal("no frames with 24-bit colour")
	}
}
