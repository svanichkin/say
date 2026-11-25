package ui

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image/color"
	"math"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/codec"
)

// The pixel window (package gui) shows this interface instead of a
// terminal. It takes the terminal output for the layout and text (in a
// monospaced font, a character a cell) and sends keys and clicks here. Video
// panes are not
// written as cells: writeChatPane marks the pane and hands the frame over
// (PixelFrame), and the window draws it at any resolution.

var pixelMode bool

// PixelMode reports whether Say draws into its window (SetPixelMode), not
// a terminal.
func PixelMode() bool { return pixelMode }

// SetPixelMode prepares the interface for the pixel window: 24-bit colour,
// no terminal input (the window sends it). Call it before the renderer
// starts.
func SetPixelMode() {
	pixelMode = true
	colorMode.Store(int32(ColorTrue))
	termBorder, termSeparator = 2, 3
	termSide, termGap = 4, 5 // the panes' frames reach some 1.5 cells out: two cells between them
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

// writePixelChat marks the chat (the conversation and the input line, or
// the status) for the window to frame: OSC 5579 with its content's rect,
// 1-based cells, and c: the frame half a row round it, as round all content.
func writePixelChat(out *strings.Builder, top, left, cols, rows, divider int) {
	if divider > 0 { // a line across, along the top of its row divider (1-based): x below it
		fmt.Fprintf(out, "\x1b]5579;%d;%d;%d;%d;c;%d\x07", top, left, cols, rows, divider)
		return
	}
	fmt.Fprintf(out, "\x1b]5579;%d;%d;%d;%d;c\x07", top, left, cols, rows)
}

// writePixelMenu marks a column of the home screen for the window to frame
// as it frames its panes (OSC 5581): its items' rect (1-based top, left,
// then cols, rows; the top may be above the screen), the frame's seed, its
// colour, and the row (1-based in the rect, 0 none) a line goes across
// the top of: + and < and x below it.
func writePixelMenu(out *strings.Builder, top, left, cols, rows int, seed uint64, c [3]int, divider int) {
	fmt.Fprintf(out, "\x1b]5581;%d;%d;%d;%d;%d;%d;%d;%d;%d\x07", top, left, cols, rows, seed, c[0], c[1], c[2], divider)
}

// writeChatFrame frames the chat's rect (1-based cells): marked for the
// window, or drawn in characters in a terminal (at).
func writeChatFrame(out *strings.Builder, h *hudCanvas, top, left, cols, rows, divider int) {
	if pixelMode {
		// The content's rows: the window frames them half a row out, as
		// every frame round content (hud.ContentFrameOf); divider: the
		// content's row (1-based) a line goes across the top of.
		writePixelChat(out, top+1, left, cols, rows-2, divider)
		return
	}
	_, _, seed := FrameSeeds()
	if gfxHUD {
		gfxChat(top, left, cols, rows)
		var cells hudCanvas
		cells.cells = map[[2]int]hudCell{}
		hudBox(&cells, top, left, cols, rows, ChatAccent(), seed, false, true)
		cells.blank()
		for k, c := range cells.cells {
			h.cells[k] = c
		}
		return
	}
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
const statusPadCols = 3

// writePixelStatusFrame puts the status lines (say "…", connecting and the
// like), nLines of them width wide from startRow, in a frame of the chat's
// kind: some room round the text, and no wider or taller than the window.
// It returns the frame's rect (its lines' cells in a terminal, the content's
// columns in the window); divider is the line (0-based) a line goes across
// the top of in the window (-1 none; a terminal draws its own).
func writePixelStatusFrame(out *strings.Builder, h *hudCanvas, width, nLines, startRow, cols, rows, divider int) (top, left, frameCols, frameRows int) {
	const padCols, padRows, minCols = statusPadCols, 1, 30
	frameCols = min(max(minCols, width+2*padCols), cols-2*termSide)
	frameRows = min(nLines+2*padRows, rows-2*termBorder)
	if frameCols < 4 || frameRows < 1 {
		return 0, 0, 0, 0
	}
	left = 1 + (cols-frameCols)/2
	top = max(1, startRow-padRows)
	writeChatFrame(out, h, top, left, frameCols, frameRows, divider+1)
	return top, left, frameCols, frameRows
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

// FrameRGB applies the display gamma and any local colour filter to a frame
// colour. The encoded frame and network stream are unchanged.
func FrameRGB(y, cb, cr byte) (r, g, b byte) {
	ri, gi, bi := videoDisplayRGB(y, cb, cr)
	return byte(ri), byte(gi), byte(bi)
}

// FrameY applies the same display gamma to the window's monochrome video.
func FrameY(y byte) byte { return videoGammaY[y] }

// Each detail step adds one row and the proportional number of columns,
// preserving the pane's aspect. The steps come from Cmd+ / Cmd- alone (an
// automatic step earned by clean incoming video, every 2 s, changed the
// picture's grid under the viewer: gone). The requested size can reach
// twice the pane size, within the codec's frame limit.
var videoDetail atomic.Int32

// SetVideoDetail sets the number of detail steps.
func SetVideoDetail(steps int) {
	videoDetail.Store(int32(min(max(0, steps), currentDetailLimit())))
	RequestRedraw()
}

// StepVideoDetail is called for one Cmd+ or Cmd- press.
func StepVideoDetail(delta int) {
	if delta == 0 {
		return
	}
	limit := currentDetailLimit()
	steps := min(int(videoDetail.Load()), limit)
	switch {
	case delta > 0 && steps < limit:
		steps++
	case delta < 0 && steps > 0:
		steps--
	}
	videoDetail.Store(int32(steps))
	RequestRedraw()
}

// currentDetailLimit uses the current window's pane as the basis for the
// steps. The window has not reported a pane before a call.
func currentDetailLimit() int {
	selfSlotMu.RLock()
	cols, rows := selfViewport.peerSlot.cols, selfViewport.peerSlot.rows
	selfSlotMu.RUnlock()
	return detailLimit(cols, rows)
}

// steppedDetailCols preserves the pane's aspect as rows change one by one.
// With an odd number of columns, some steps add one column and others two.
func steppedDetailCols(cols, rows, steps int) int {
	if rows <= 0 {
		return cols
	}
	return (cols*(rows+steps) + rows/2) / rows
}

// detailLimit allows the original 2x pane size, clipped by the codec's limit.
// The source is not a hard pixel-for-pixel ceiling: a 4x8 glyph has one shape
// and two colours, so more cells can still approximate the same source better.
func detailLimit(cols, rows int) int {
	if cols <= 0 || rows <= 0 {
		return 0
	}
	maxCols := min(2*cols, codec.MaxFrameCols)
	steps := max(0, min(rows, codec.MaxFrameRows-rows))
	for steps > 0 && steppedDetailCols(cols, rows, steps) > maxCols {
		steps--
	}
	return steps
}

// detailSize is a pane's requested frame size. One step adds one row.
func detailSize(cols, rows int) (int, int) {
	if pixelMode {
		steps := min(detailLimit(cols, rows), int(videoDetail.Load()))
		cols = steppedDetailCols(cols, rows, steps)
		rows += steps
	}
	return min(cols, codec.MaxFrameCols), min(rows, codec.MaxFrameRows)
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
// peer's), and, for a menu in it, the row a line goes across the top of.
func writePixelPane(out *strings.Builder, frame *codec.TextFrame, top, left, cols, rows int, remote bool, divider int) {
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
	if divider > 0 { // a line across, along the top of its row divider (1-based): a menu's
		fmt.Fprintf(out, "\x1b]5577;%d;%d;%d;%d;%d;%s;%d\x07", top, left, cols, rows, id, kind, divider)
		return
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

// staticPixelFrame is the window's no-signal noise for a pane of cols x
// rows cells: a TV's snow in pixels (StaticPixels, 4 x 8 a cell, as the
// window draws cells), not glyphs.
func staticPixelFrame(cols, rows int, now time.Time) *codec.TextFrame {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	n := cols * rows
	cells := make([]byte, 7*n)
	return &codec.TextFrame{Cols: cols, Rows: rows, Width: cols * 4, Height: rows * 8,
		Glyphs: cells[:n:n], FgYCbCr: cells[n : 4*n : 4*n], BgYCbCr: cells[4*n:],
		Pixels: StaticPixels(cols*4, rows*8, now)}
}

// StaticPixels is a w x h picture of a TV's snow (the window's no-signal
// noise, and a picture lost on a bad path): each grain a pixel, smeared a
// little along its row as an analog picture's; each row a little brighter
// or darker than the next, now and then one much brighter; a darker band
// rolling down slowly (the hum). Grey: the window's effects tone it as any
// picture.
func StaticPixels(w, h int, now time.Time) *codec.Pixels {
	if w <= 0 || h <= 0 {
		return nil
	}
	buf := make([]byte, 3*w*h)
	y, chroma := buf[:w*h:w*h], buf[w*h:]
	for i := range chroma {
		chroma[i] = 128
	}
	rng := rand.NewPCG(uint64(now.UnixNano()), uint64(nextNoiseSeed()))
	// The hum: a band of a third of the picture, once down in 3 s.
	t := float64(now.UnixMilli()%3000) / 3000
	for r := range h {
		row := y[r*w : (r+1)*w]
		bits := rng.Uint64()
		gain := 0.88 + float64(bits&63)/63*0.24 // the row's own
		if bits>>6&255 == 0 {
			gain = 1.6 // a bright streak
		}
		if d := math.Abs(float64(r)/float64(h) - t); min(d, 1-d) < 1.0/6 {
			gain *= 0.75
		}
		prev := 128.0
		for x := 0; x < w; x += 4 {
			bits = rng.Uint64()
			for k := 0; k < 4 && x+k < w; k++ {
				// Two uniforms summed: most grains grey, some black or white.
				v := float64(bits>>(16*k)&255) + float64(bits>>(16*k+8)&255) - 255
				v = 128 + v*0.9
				prev = 0.6*v + 0.4*prev                                // smeared along the row
				row[x+k] = byte(math.Min(255, math.Max(0, prev*gain))) // the package has its own int max
			}
		}
	}
	return &codec.Pixels{W: w, H: h, Y: y, Cb: chroma[:w*h], Cr: chroma[w*h:]}
}

// testCardPixelFrame is the TV test card for the window, drawn in pixels
// (SMPTE colour bars, testCardPixels), not in the cells' glyphs: a frame
// of cols x rows cells holding only the picture, at twice the cells'
// pixels across and down (its edges stay sharp drawn larger). Built once
// for the pane's size, again only when the size changes.
func testCardPixelFrame(cols, rows int) *codec.TextFrame {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	testCard.Lock()
	defer testCard.Unlock()
	if f := testCard.frame; f != nil && f.Cols == cols && f.Rows == rows {
		return f
	}
	n := cols * rows
	cells := make([]byte, 7*n)
	testCard.frame = &codec.TextFrame{Cols: cols, Rows: rows, Width: cols * 4, Height: rows * 8,
		Glyphs: cells[:n:n], FgYCbCr: cells[n : 4*n : 4*n], BgYCbCr: cells[4*n:],
		Pixels: testCardPixels(cols*8, rows*16, 1, 1)}
	return testCard.frame
}

// testCard is the window's test card for the size it was built for.
var testCard struct {
	sync.Mutex
	frame *codec.TextFrame
}

// testCardPixels is the TV test card at w x h pixels, the window's and
// (made cells) the terminal's: seven bars of full colour (white, yellow,
// cyan, green, magenta, red, blue), each as wide, over four fifths of the
// height; a strip under them (blue, black, magenta, black, cyan, black,
// white); a strip of grey, a ramp from white to black over its left half
// and ten steps from white to black over its right; at the bottom dark
// grey, black, grey, then black. Its edges fall on multiples of sx across
// and sy down (the terminal's cells, 4 x 8: a cell of two colours became a
// glyph's jagged edge; the window's 1), the ramp a step each sx.
func testCardPixels(w, h, sx, sy int) *codec.Pixels {
	type rgb [3]uint8
	p := &codec.Pixels{W: w, H: h, Y: make([]byte, w*h), Cb: make([]byte, w*h), Cr: make([]byte, w*h)}
	snap := func(v, s int) int { return (v + s/2) / s * s }
	set := func(x0, y0, x1, y1 int, c rgb) {
		x0, x1, y0, y1 = snap(x0, sx), snap(x1, sx), snap(y0, sy), snap(y1, sy)
		x0, y0, x1, y1 = max(0, x0), max(0, y0), min(w, x1), min(h, y1)
		yy, u, v := color.RGBToYCbCr(c[0], c[1], c[2])
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				i := y*w + x
				p.Y[i], p.Cb[i], p.Cr[i] = yy, u, v
			}
		}
	}
	bars := func(y0, y1 int, colours []rgb) {
		for k, c := range colours {
			set(k*w/len(colours), y0, (k+1)*w/len(colours), y1, c) // each as wide
		}
	}
	// The bands' edges, in steps of sy, each band at least a step high.
	n := h / sy
	b1 := max(1, min(n-3, (n*795+500)/1000))
	b2 := max(b1+1, min(n-2, (n*852+500)/1000))
	b3 := max(b2+1, min(n-1, (n*911+500)/1000))
	y1, y2, y3 := b1*sy, b2*sy, b3*sy
	bars(0, y1, []rgb{{255, 255, 255}, {255, 230, 0}, {0, 255, 255}, {40, 255, 0}, {255, 60, 255}, {255, 40, 40}, {0, 0, 230}})
	bars(y1, y2, []rgb{{0, 0, 230}, {0, 0, 0}, {255, 60, 255}, {0, 0, 0}, {90, 255, 255}, {0, 0, 0}, {255, 255, 255}})
	half := snap(w/2, sx)
	for x := 0; x < half; x += sx {
		v := uint8(255 - 255*x/max(1, half-sx)) // the ramp, a step each sx
		set(x, y2, x+sx, y3, rgb{v, v, v})
	}
	for k := range 10 {
		v := uint8(255 - 255*k/9) // the steps
		set(half+k*(w-half)/10, y2, half+(k+1)*(w-half)/10, y3, rgb{v, v, v})
	}
	bars(y3, h, []rgb{{60, 60, 60}, {0, 0, 0}, {85, 85, 85}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}})
	return p
}
