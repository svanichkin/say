package ui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/hud"
)

// The HUD as an image, in terminals that show images over their text (the
// kitty graphics protocol: kitty, Ghostty). The layout is the frames' in
// characters (termHUD); instead of drawing them in characters, the screen
// collects what they frame (a scene: the panes, the chat, the brackets, the
// window's border), and package hud draws it as the window does, with the
// same geometry and glow. The image covers the whole screen, transparent but
// for the HUD, laid over the text as the window lays its frames over the
// cells. It is sent again only when the scene changes, or after the screen
// was cleared (which takes images with it).

// gfxHUD: the HUD goes as an image.
var gfxHUD bool

// kittyImageID is our image's id, and its placement's.
const kittyImageID = 7031

// kittyChunk is the most base64 one escape sequence of the protocol carries.
const kittyChunk = 4096

// kittyGraphics reports whether the terminal shows images by the kitty
// graphics protocol over its text. SAY_GRAPHICS=0 turns it off, =kitty
// forces it (for a terminal not recognised).
func kittyGraphics() bool {
	switch os.Getenv("SAY_GRAPHICS") {
	case "0", "off", "no":
		return false
	case "kitty":
		return true
	}
	if os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return false // a multiplexer passes no images on
	}
	return os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" ||
		strings.EqualFold(os.Getenv("TERM_PROGRAM"), "ghostty")
}

// setGraphics turns the HUD image on or off (with the frames' layout only).
func setGraphics(on bool) {
	gfxHUD = on && termHUD
	if gfxHUD {
		// Leaving, the image goes: the terminal might keep it.
		device.SetExitSequence(kittyDelete())
	}
}

// kittyDelete deletes every image on the screen, and their data.
func kittyDelete() string { return "\x1b_Ga=d,d=A,q=2\x1b\\" }

// gfxScene is the scene of the screen being drawn (the render goroutine's).
var gfxScene hud.Scene

// gfxBegin starts collecting a screen's scene.
func gfxBegin() { gfxScene = hud.Scene{} }

// gfxPane frames a video pane (1-based cells) in the scene.
func gfxPane(top, left, cols, rows int, remote bool) {
	self, peer := Accents()
	selfSeed, peerSeed, _ := FrameSeeds()
	c, seed := self, selfSeed
	if remote {
		c, seed = peer, peerSeed
	}
	gfxScene.Panes = append(gfxScene.Panes, hud.Pane{
		Rect: hud.Rect{Row: top - 1, Col: left - 1, Cols: cols, Rows: rows}, Remote: remote,
		Colour: rgba(c), Seed: seed,
	})
}

// gfxChat frames the chat (or the status) at its rect (1-based cells).
func gfxChat(top, left, cols, rows int) {
	_, _, seed := FrameSeeds()
	gfxScene.Panes = append(gfxScene.Panes, hud.Pane{
		Rect: hud.Rect{Row: top - 1, Col: left - 1, Cols: cols, Rows: rows}, Chat: true,
		Colour: rgba(ChatAccent()), Seed: seed,
	})
}

// gfxBracket brackets a row (as writePixelBracket's arguments).
func gfxBracket(row, left, width int, spans ...[2]int) {
	gfxScene.Brackets = append(gfxScene.Brackets, hud.Bracket{Row: row - 1, Col: left - 1, Cols: width, Spans: append([][2]int(nil), spans...)})
}

// gfxBorder draws the window's own border in the scene.
func gfxBorder() {
	self, _, _ := FrameSeeds()
	gfxScene.Border, gfxScene.BorderSeed = true, self
}

func rgba(c [3]int) color.RGBA { return color.RGBA{uint8(c[0]), uint8(c[1]), uint8(c[2]), 255} }

// gfxGrid is the terminal's cell grid in pixels for cols x rows cells: what
// it reports, or a guess (the terminal scales the image to the cells it is
// laid on anyway).
func gfxGrid(cols, rows int) hud.Grid {
	w, h := device.TermPixelSize()
	cw, ch := 10, 20
	if w > 0 && h > 0 && cols > 0 && rows > 0 {
		cw, ch = max(4, w/cols), max(8, h/rows)
	}
	return hud.Grid{CW: cw, CH: ch}
}

// gfxVideoMask is the cells of the video panes outside their frames' inner
// line (the cut corners), which the video leaves to the HUD.
func gfxVideoMask(gr hud.Grid) *hudCanvas {
	mask := newHUDCanvas()
	for _, p := range gfxScene.Panes {
		if p.Chat {
			continue
		}
		inner := p.Inner(gr)
		for r := p.Row; r < p.Row+p.Rows; r++ {
			for c := p.Col; c < p.Col+p.Cols; c++ {
				x, y := float64(c*gr.CW)+float64(gr.CW)/2, float64(r*gr.CH)+float64(gr.CH)/2
				if !insidePolygon(inner, x, y) {
					mask.set(r+1, c+1, ' ', hudFg)
				}
			}
		}
	}
	return mask
}

// insidePolygon reports whether x, y is inside poly (even-odd).
func insidePolygon(poly []hud.Pt, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a[1] > y) != (b[1] > y) && x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	return in
}

// The image goes in two parts. The frames and the border change only with
// the layout: they are drawn on another goroutine (a few hundred ms for a
// large screen), and until they are ready the screen goes without them,
// which also passes over the sizes a window goes through while dragged.
// The brackets' row changes with the captions' widths, every few seconds:
// it is a strip of its own, one row high, drawn at once.
var gfx struct {
	mu     sync.Mutex
	want   gfxJob // the frames wanted
	busy   bool   // a goroutine draws them
	ready  gfxPNG // the last drawn
	shown  string // the frames' key on the screen
	strips []string
}

type gfxJob struct {
	key        string
	scene      hud.Scene
	grid       hud.Grid
	cols, rows int
}

type gfxPNG struct {
	key string
	png []byte
}

// gfxFramesID is the frames' image; the brackets' strips follow it.
const gfxFramesID = kittyImageID

// gfxOutput is what to write after a screen (cols x rows) for its HUD
// images: nothing when the terminal shows them already. cleared: the
// screen's output clears the terminal, images with it.
func gfxOutput(cols, rows int, cleared bool) string {
	if !gfxHUD {
		return ""
	}
	gr := gfxGrid(cols, rows)
	frames := hud.Scene{Panes: gfxScene.Panes, Border: gfxScene.Border, BorderSeed: gfxScene.BorderSeed}
	key := fmt.Sprintf("%dx%d %+v %+v", cols, rows, gr, frames)
	var b strings.Builder
	gfx.mu.Lock()
	if cleared {
		gfx.shown, gfx.strips = "", nil
	}
	if gfx.want.key != key {
		gfx.want = gfxJob{key: key, scene: frames, grid: gr, cols: cols, rows: rows}
		if !gfx.busy {
			gfx.busy = true
			go gfxDraw()
		}
	}
	switch {
	case gfx.shown == key:
	case gfx.ready.key == key:
		b.WriteString(kittyPlace(gfxFramesID, gfx.ready.png, 1, 1, cols, rows))
		gfx.shown = key
	case gfx.shown != "":
		b.WriteString(kittyDeleteID(gfxFramesID)) // the old layout's
		gfx.shown = ""
	}
	// The brackets' strips.
	for i, br := range gfxScene.Brackets {
		strip := hud.Bracket{Cols: br.Cols, Spans: br.Spans}
		skey := fmt.Sprintf("%d,%d %+v %+v", br.Row, br.Col, gr, strip)
		if i < len(gfx.strips) && gfx.strips[i] == skey {
			continue
		}
		img := hud.Render(hud.Scene{Brackets: []hud.Bracket{strip}}, gr, br.Cols*gr.CW, gr.CH)
		var buf bytes.Buffer
		if (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img) != nil {
			continue
		}
		b.WriteString(kittyPlace(gfxFramesID+1+i, buf.Bytes(), br.Row+1, br.Col+1, br.Cols, 1))
		for len(gfx.strips) <= i {
			gfx.strips = append(gfx.strips, "")
		}
		gfx.strips[i] = skey
	}
	for i := len(gfxScene.Brackets); i < len(gfx.strips); i++ {
		if gfx.strips[i] != "" {
			b.WriteString(kittyDeleteID(gfxFramesID + 1 + i))
		}
	}
	gfx.strips = gfx.strips[:min(len(gfx.strips), len(gfxScene.Brackets))]
	gfx.mu.Unlock()
	return b.String()
}

// gfxDraw draws the frames wanted, and again while newer ones are wanted
// meanwhile; then the screen is drawn again, with them.
func gfxDraw() {
	for {
		gfx.mu.Lock()
		job := gfx.want
		gfx.mu.Unlock()
		img := hud.Render(job.scene, job.grid, job.cols*job.grid.CW, job.rows*job.grid.CH)
		var buf bytes.Buffer
		err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img)
		gfx.mu.Lock()
		if err == nil {
			gfx.ready = gfxPNG{key: job.key, png: buf.Bytes()}
		}
		if gfx.want.key == job.key {
			gfx.busy = false
			gfx.mu.Unlock()
			RequestRedraw()
			return
		}
		gfx.mu.Unlock()
	}
}

func kittyDeleteID(id int) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

// kittyPlace sends a PNG as image id and lays it over cols x rows cells
// from row, col (1-based), above the text, replacing the one before; the
// cursor stays where it was. q=2: the terminal answers nothing (an answer
// would come as input).
func kittyPlace(id int, pngData []byte, row, col, cols, rows int) string {
	data := base64.StdEncoding.EncodeToString(pngData)
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b7\x1b[%d;%dH", row, col)
	for i := 0; i < len(data) || i == 0; i += kittyChunk {
		chunk := data[i:min(len(data), i+kittyChunk)]
		more := 0
		if i+kittyChunk < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,f=100,t=d,i=%d,p=1,q=2,C=1,z=1,c=%d,r=%d,m=%d;%s\x1b\\", id, cols, rows, more, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	b.WriteString("\x1b8")
	return b.String()
}
