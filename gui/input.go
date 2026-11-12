package gui

import (
	"time"

	"github.com/svanichkin/say/codec"
)

// Input connects the window to the interface: what the user does goes out,
// the frames of the video panes, the colours and the ping come in.
type Input struct {
	Key       func(r rune)       // a typed character; '\r' Enter, 0x7f Backspace
	Paste     func(s string)     // pasted text
	Click     func(col, row int) // a click on a cell (1-based)
	Interrupt func()             // Ctrl+C or the window closed
	Resized   func()             // the screen changed size or font: redraw
	Detail    func(d int)        // video detail changed (1, 2 or 4)

	Frame    func(id uint64) *codec.TextFrame    // the frame a pane mark names
	FrameRGB func(y, cb, cr byte) (r, g, b byte) // a frame colour as shown

	Accents func() (self, peer [3]int)       // the accent colours: ours, the peer's
	Chat    func() [3]int                    // the chat frame's colour
	Brick   func(frame [3]int) [3]int        // the bricks' colour on a frame of that colour
	Mono    func() bool                      // -bw: each pane in its owner's colour
	Seeds   func() (self, peer, chat uint64) // the frames' seeds
	Latency func() time.Duration             // the round trip to the peer (0: unknown)
}
