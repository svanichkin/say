package ui

import (
	"strings"
	"testing"
	"time"
)

// The version is written on the right, on the row under the chat's input:
// in the frame layout under the frame, in the plain one the bottom margin.
func TestVersionUnderTheInput(t *testing.T) {
	SetVersion("v9.8.7")
	defer SetVersion("")
	defer func() {
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	for _, hud := range []bool{false, true} {
		setTermHUD(hud)
		cols, rows := 120, 44
		paneCache = screenCache{}
		v := newVterm(cols, rows)
		v.apply(renderChatScreen(cols, rows))
		row := rows
		if hud {
			row = rows - 1
		}
		var line strings.Builder
		for c := 1; c <= cols; c++ {
			if r := v.cells[(row-1)*cols+c-1].r; r != 0 {
				line.WriteRune(r)
			} else {
				line.WriteByte(' ')
			}
		}
		text := line.String()
		at := strings.Index(text, "v9.8.7")
		if at < cols/2 {
			t.Fatalf("hud %v: no version on the right of row %d: %q", hud, row, text)
		}
	}
	setTermHUD(false)
}
