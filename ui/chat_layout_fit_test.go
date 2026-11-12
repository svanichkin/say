package ui

import (
	"testing"
	"time"
)

// The call screen is drawn at every size a window or terminal can have:
// when it was not, the screen kept what it showed before the call (the
// status) while the call went on.
func TestChatScreenDrawsAtEverySize(t *testing.T) {
	SetChatActions(func(string) {}, func(string) {})
	defer func() {
		SetChatActions(nil, nil)
		// Drawing the panes started their no-signal clocks: leave them as
		// other tests expect them.
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	for _, mode := range []struct {
		name                         string
		pixel                        bool
		side, gap, border, separator int
	}{
		{"terminal", false, 1, 1, 1, 1},
		{"window", true, 4, 6, 2, 3},
	} {
		was, ws, wg, wb, wsep := pixelMode, termSide, termGap, termBorder, termSeparator
		pixelMode, termSide, termGap, termBorder, termSeparator = mode.pixel, mode.side, mode.gap, mode.border, mode.separator
		for cols := 40; cols <= 240; cols += 3 {
			for rows := 16; rows <= 100; rows++ {
				if renderChatScreen(cols, rows) == "" {
					t.Errorf("%s: no call screen at %dx%d", mode.name, cols, rows)
				}
			}
		}
		pixelMode, termSide, termGap, termBorder, termSeparator = was, ws, wg, wb, wsep
	}
}
