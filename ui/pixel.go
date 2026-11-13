package ui

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/logs"
)

// The pixel window (package gui) shows this interface instead of a
// terminal. It takes the terminal output for the layout and text (in a
// monospaced font, a character a cell) and sends keys and clicks here. Video
// panes are not
// written as cells: writeChatPane marks the pane and hands the frame over
// (PixelFrame), and the window draws it at any resolution.

var pixelMode bool

// SetPixelMode prepares the interface for the pixel window: 24-bit colour,
// no terminal input (the window sends it). Call it before the renderer
// starts.
func SetPixelMode() {
	pixelMode = true
	colorMode.Store(int32(ColorTrue))
	termBorder, termSeparator = 2, 3
	termSide, termGap = 4, 6
	SetAccentAddresses("", "")
}

// The window's two accent colours, from the colour filter presets: ours (our
// messages, the input line and its cursor, our pane) and the peer's (its
// messages, its pane). Each person's colour follows from their address (and
// so their key): the same in both windows and in every call.
var accents atomic.Pointer[[2][3]int]

// accentPresets are the colour filters bright enough on black (blue and
// purple lightened).
var accentPresets = [][3]int{
	{255, 64, 64}, {255, 128, 0}, {255, 230, 0}, {0, 255, 64},
	{0, 255, 255}, {80, 150, 255}, {200, 90, 255}, {255, 64, 160},
}

// BrickColour is the preset furthest in hue from c: the bricks on a frame
// of colour c stand out from it, and so does the text in the status frame.
func BrickColour(c [3]int) [3]int {
	hue := func(c [3]int) float64 {
		r, g, b := float64(c[0]), float64(c[1]), float64(c[2])
		mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
		if mx == mn {
			return 0
		}
		var h float64
		switch mx {
		case r:
			h = math.Mod((g-b)/(mx-mn), 6)
		case g:
			h = (b-r)/(mx-mn) + 2
		default:
			h = (r-g)/(mx-mn) + 4
		}
		return h * 60
	}
	h0 := hue(c)
	best, bestD := accentPresets[0], -1.0
	for _, p := range accentPresets {
		d := math.Abs(hue(p) - h0)
		d = math.Min(d, 360-d)
		if d > bestD {
			best, bestD = p, d
		}
	}
	return best
}

// SetAccentAddresses sets the accent colours from our address and the
// peer's ("" while it is unknown). When the two come out the same, the
// larger address takes the next colour: both sides apply the same rule, so
// both see the same pair.
func SetAccentAddresses(self, peer string) {
	n := len(accentPresets)
	a, b := addressColour(self), addressColour(peer)
	if peer == "" {
		b = (a + n/2) % n // until the peer is known: far from ours
	}
	if a == b {
		if bytes.Compare(addressKey(self), addressKey(peer)) > 0 {
			a = (a + 1) % n
		} else {
			b = (b + 1) % n
		}
	}
	accents.Store(&[2][3]int{accentPresets[a], accentPresets[b]})
	// The chat's frame: the first colour neither of us has.
	c := 0
	for c == a || c == b {
		c++
	}
	chatAccent.Store(&accentPresets[c])

	// Keep colours tied to addresses, but give this launch its own geometry.
	// The salt is sampled once: redraws and reconnects cannot reshuffle it.
	ha, hb := uint64(addressHash(self)), uint64(addressHash(peer))
	if peer == "" {
		hb = ha ^ 0x9e3779b9
	}
	seeds.Store(&[3]uint64{
		frameSeed(ha | 1<<32), frameSeed(hb | 1<<32), frameSeed((ha ^ hb) | 2<<32),
	})
	RequestRedraw()
}

var seeds atomic.Pointer[[3]uint64]

var frameLaunchSalt = rand.Uint64()

func frameSeed(identity uint64) uint64 {
	x := identity + frameLaunchSalt
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// FrameSeeds returns the seeds the window generates the frames from: ours,
// the peer's and the chat's.
func FrameSeeds() (self, peer, chat uint64) {
	s := seeds.Load()
	if s == nil {
		return 1, 2, 3
	}
	return s[0], s[1], s[2]
}

// addressKey is an address in a comparable form: its 16 bytes when it is an
// IP address.
func addressKey(addr string) []byte {
	if ip := net.ParseIP(strings.Trim(strings.TrimSpace(addr), "[]")); ip != nil {
		return ip.To16()
	}
	return []byte(addr)
}

// addressHash is a hash of an address (0 for none).
func addressHash(addr string) uint32 {
	if addr == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write(addressKey(addr))
	return h.Sum32()
}

// addressColour picks a preset for an address.
func addressColour(addr string) int {
	return int(addressHash(addr) % uint32(len(accentPresets)))
}

var chatAccent atomic.Pointer[[3]int]

// ChatAccent is the colour of the chat's frame: neither ours nor the peer's.
func ChatAccent() [3]int {
	if c := chatAccent.Load(); c != nil {
		return *c
	}
	return accentPresets[3]
}

// writePixelChat marks the chat (the conversation and the input line) for
// the window to frame: OSC 5579 with its rect, 1-based cells.
func writePixelChat(out *strings.Builder, top, left, cols, rows int) {
	fmt.Fprintf(out, "\x1b]5579;%d;%d;%d;%d\x07", top, left, cols, rows)
}

// writeChatFrame frames the chat's rect (1-based cells): marked for the
// window, or drawn in characters in a terminal (at).
func writeChatFrame(out *strings.Builder, h *hudCanvas, top, left, cols, rows int) {
	if pixelMode {
		writePixelChat(out, top, left, cols, rows)
		return
	}
	_, _, seed := FrameSeeds()
	hudBox(h, top, left, cols, rows, ChatAccent(), seed, false, true)
}

// outAt is a cellAt writing everything to out.
func outAt(out *strings.Builder) cellAt {
	return func(row, col int) *strings.Builder {
		writeCursorTo(out, row, col)
		return out
	}
}

// statusHeading puts what comes after the first ": " of the first line on
// the next one: the part before it is the heading ("Dial failed", "Error").
// Colons with no space after them (an address) do not count.
func statusHeading(msg string) string {
	first, rest, _ := strings.Cut(msg, "\n")
	head, tail, ok := strings.Cut(first, ": ")
	if !ok || strings.TrimSpace(head) == "" || strings.TrimSpace(tail) == "" {
		return msg
	}
	out := head + "\n" + strings.TrimSpace(tail)
	if rest != "" {
		out += "\n" + rest
	}
	return out
}

// statusPadCols is the room left and right of the status text in its frame.
const statusPadCols = 4

// writePixelStatusFrame puts the status lines (say "…", connecting and the
// like), nLines of them width wide from startRow, in a frame of the chat's
// kind: some room round the text, and no wider or taller than the window.
func writePixelStatusFrame(out *strings.Builder, h *hudCanvas, width, nLines, startRow, cols, rows int) {
	const padCols, padRows, minCols = statusPadCols, 1, 30
	frameCols := min(max(minCols, width+2*padCols), cols-2*termSide)
	frameRows := min(nLines+2*padRows, rows-2*termBorder)
	if frameCols < 4 || frameRows < 1 {
		return
	}
	left := 1 + (cols-frameCols)/2
	top := max(1, startRow-padRows)
	writeChatFrame(out, h, top, left, frameCols, frameRows)
}

// Accents returns our accent colour and the peer's.
func Accents() (self, peer [3]int) {
	a := accents.Load()
	if a == nil {
		return accentPresets[1], accentPresets[4]
	}
	return a[0], a[1]
}

// accentSGR is the SGR sequence for an accent colour as the foreground.
func accentSGR(c [3]int) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
}

// KeyInput types r, as a key in the terminal would ('\r' sends, 0x7f
// erases).
func KeyInput(r rune) { chatKey(r) }

// PasteInput pastes s into the chat input (or sends the file it names).
func PasteInput(s string) { chatPaste(s) }

// ClickAt clicks the cell at col, row (1-based, as terminal mouse reports).
func ClickAt(col, row int) {
	handleMouseEvent(mouseEvent{button: 0, col: col, row: row, pressed: true})
}

// Interrupt stops Say, as Ctrl+C in the terminal does.
func Interrupt() { interruptSelf() }

// FrameRGB is the colour a frame's YCbCr shows as, with the colour filter
// (-orange and the like) applied.
func FrameRGB(y, cb, cr byte) (r, g, b byte) {
	ri, gi, bi := applyColorFilter(ycbcrToRGB(y, cb, cr))
	return byte(ri), byte(gi), byte(bi)
}

// Video detail: in the pixel window a pane of n x m cells can hold a frame
// of n*d x m*d cells, d = 1, 2 or 4 (Cmd+ and Cmd-, and one step more while
// the video comes in clean). The peer is asked for frames that size and the
// local preview is encoded so.
var videoDetail atomic.Int32

// SetVideoDetail sets the detail (1, 2 or 4).
func SetVideoDetail(d int) {
	videoDetail.Store(int32(max(1, min(4, d))))
	RequestRedraw()
}

func detail() int {
	if !pixelMode {
		return 1
	}
	d := max(1, int(videoDetail.Load()))
	if detailBoost.Load() {
		d = min(4, d*2)
	}
	return d
}

// While the peer's video comes in clean, the window asks for it one step
// finer than the detail chosen (as one Cmd+ would); when it starts losing
// frames, one step back, and it waits a while before trying again.
const (
	boostCleanLoss = 0.01             // a second this clean counts as clean
	boostAfter     = 10 * time.Second // of clean seconds in a row
	boostDropLoss  = 0.04             // a second this lossy drops the step
	boostRetry     = time.Minute      // after a drop, before the next try
)

var detailBoost atomic.Bool

var boostState struct {
	sync.Mutex
	cleanSince time.Time
	retryAt    time.Time
}

// ResetDetailBoost drops the extra step of detail and what earned it.
func ResetDetailBoost() {
	boostState.Lock()
	boostState.cleanSince, boostState.retryAt = time.Time{}, time.Time{}
	boostState.Unlock()
	detailBoost.Store(false)
}

// noteVideoLoss weighs a second's loss of the incoming video (lost, broken
// or late frames) for the detail boost.
func noteVideoLoss(loss float64, now time.Time) {
	if !pixelMode {
		return
	}
	boostState.Lock()
	defer boostState.Unlock()
	if loss > boostCleanLoss {
		boostState.cleanSince = time.Time{}
		if loss >= boostDropLoss && detailBoost.Load() {
			detailBoost.Store(false)
			boostState.retryAt = now.Add(boostRetry)
			logs.LogV("[video] incoming video loss %.0f%%: detail one step down", loss*100)
			RequestRedraw()
		}
		return
	}
	if boostState.cleanSince.IsZero() {
		boostState.cleanSince = now
	}
	if !detailBoost.Load() && now.Sub(boostState.cleanSince) >= boostAfter && !now.Before(boostState.retryAt) {
		detailBoost.Store(true)
		logs.LogV("[video] incoming video clean for %v: detail one step up", boostAfter)
		RequestRedraw()
	}
}

// detailSize is a pane's frame size at the current detail, within what the
// codec carries.
func detailSize(cols, rows int) (int, int) {
	d := detail()
	return min(cols*d, codec.MaxFrameCols), min(rows*d, codec.MaxFrameRows)
}

// Frames handed to the window, by the id in the pane mark. Only the latest
// few are kept: the window draws the frame of the last mark it read.
var pixelFrames struct {
	sync.Mutex
	seq    uint64
	frames map[uint64]*codec.TextFrame
}

// PixelFrame returns a frame handed to the window (nil if too old).
func PixelFrame(id uint64) *codec.TextFrame {
	pixelFrames.Lock()
	defer pixelFrames.Unlock()
	return pixelFrames.frames[id]
}

// writePixelPane marks a video pane in the output: OSC 5577 with its rect
// (1-based cells), the id of its frame and whose it is (L ours, R the
// peer's).
func writePixelPane(out *strings.Builder, frame *codec.TextFrame, top, left, cols, rows int, remote bool) {
	pixelFrames.Lock()
	if pixelFrames.frames == nil {
		pixelFrames.frames = map[uint64]*codec.TextFrame{}
	}
	pixelFrames.seq++
	id := pixelFrames.seq
	pixelFrames.frames[id] = frame
	delete(pixelFrames.frames, id-16)
	pixelFrames.Unlock()
	kind := "L"
	if remote {
		kind = "R"
	}
	fmt.Fprintf(out, "\x1b]5577;%d;%d;%d;%d;%d;%s\x07", top, left, cols, rows, id, kind)
}

// writePixelBracket marks a row for brackets (OSC 5580): 1-based row and
// column, its width, then where each piece of text starts and ends in it
// (0-based columns of the row). The window draws a block at each end and a
// line between the texts.
func writePixelBracket(out *strings.Builder, row, left, width int, spans ...[2]int) {
	if !pixelMode {
		hudBracket(out, row, left, width, spans...)
		return
	}
	fmt.Fprintf(out, "\x1b]5580;%d;%d;%d", row, left, width)
	for _, sp := range spans {
		fmt.Fprintf(out, ";%d;%d", sp[0], sp[1])
	}
	out.WriteString("\x07")
}

// AccentMono reports whether the window tints each pane in its owner's
// accent colour: with -bw (black and white video), rather than grey.
func AccentMono() bool {
	spec := activeColorFilter.Load()
	return pixelMode && spec != nil && spec.key == "bw"
}
