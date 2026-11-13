package ui

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/codec"
)

// vterm is a minimal terminal: cursor moves, 24-bit colours, reset, text.
type vterm struct {
	cols, rows int
	cells      []screenCell
}

func newVterm(cols, rows int) *vterm {
	return &vterm{cols: cols, rows: rows, cells: make([]screenCell, cols*rows)}
}

func (t *vterm) apply(s string) {
	row, col := 1, 1
	var fg, bg [3]int
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			params := strings.Split(s[i+2:j], ";")
			n := func(k int) int { v, _ := strconv.Atoi(params[k]); return v }
			switch s[j] {
			case 'H':
				row, col = 1, 1
				if params[0] != "" {
					row, col = n(0), n(1)
				}
			case 'C':
				if params[0] == "" {
					col++
				} else {
					col += n(0)
				}
			case 'K':
				for c := 1; c <= t.cols; c++ {
					if row >= 1 && row <= t.rows {
						t.cells[(row-1)*t.cols+c-1] = screenCell{}
					}
				}
			case 'm':
				for k := 0; k < len(params); {
					switch {
					case params[k] == "0":
						fg, bg = [3]int{}, [3]int{}
						k++
					case params[k] == "38" && k+4 < len(params):
						fg = [3]int{n(k + 2), n(k + 3), n(k + 4)}
						k += 5
					case params[k] == "48" && k+4 < len(params):
						bg = [3]int{n(k + 2), n(k + 3), n(k + 4)}
						k += 5
					default:
						k++
					}
				}
			}
			i = j + 1
			continue
		}
		r, size := []rune(s[i:])[0], len(string([]rune(s[i:])[0]))
		if row >= 1 && row <= t.rows && col >= 1 && col <= t.cols {
			cell := screenCell{r: r, fg: fg, bg: bg, set: true}
			if isBlankGlyph(r) {
				cell.fg = cell.bg // invisible
			}
			t.cells[(row-1)*t.cols+col-1] = cell
		}
		col++
		i += size
	}
}

func randomTextFrame(rng *rand.Rand, cols, rows int) *codec.TextFrame {
	f := &codec.TextFrame{Cols: cols, Rows: rows, Glyphs: make([]byte, cols*rows),
		FgYCbCr: make([]byte, cols*rows*3), BgYCbCr: make([]byte, cols*rows*3)}
	rng.Read(f.FgYCbCr)
	rng.Read(f.BgYCbCr)
	for i := range f.Glyphs {
		f.Glyphs[i] = byte(rng.Intn(len(glyphCodepoints)))
	}
	return f
}

// changeCells alters n random cells of a copy of f.
func changeCells(rng *rand.Rand, f *codec.TextFrame, n int) *codec.TextFrame {
	g := *f
	g.Glyphs = append([]byte(nil), f.Glyphs...)
	g.FgYCbCr = append([]byte(nil), f.FgYCbCr...)
	g.BgYCbCr = append([]byte(nil), f.BgYCbCr...)
	for ; n > 0; n-- {
		i := rng.Intn(len(g.Glyphs))
		g.Glyphs[i] = byte(rng.Intn(len(glyphCodepoints)))
		g.FgYCbCr[i*3] ^= 0x55
	}
	return &g
}

func TestPaneDiffRendersTheSameScreenAsFullFrames(t *testing.T) {
	const cols, rows, top, left = 50, 20, 3, 5
	rng := rand.New(rand.NewSource(1))
	var cache screenCache
	cache.prepare(80, 30, time.Now())
	diffTerm, fullTerm := newVterm(80, 30), newVterm(80, 30)

	frame := randomTextFrame(rng, cols, rows)
	for step := 0; step < 30; step++ {
		var diff, full strings.Builder
		writeTextFrameRegion(&diff, frame, top, left, 0, 0, cols, rows, &cache)
		writeTextFrameRegion(&full, frame, top, left, 0, 0, cols, rows, nil)
		diffTerm.apply(diff.String())
		fullTerm.apply(full.String())
		for i := range fullTerm.cells {
			if diffTerm.cells[i] != fullTerm.cells[i] {
				t.Fatalf("step %d: cell %d differs: diff %+v, full %+v", step, i, diffTerm.cells[i], fullTerm.cells[i])
			}
		}
		if step > 0 && diff.Len()*5 > full.Len() {
			t.Fatalf("step %d: diff output %d bytes for 10 changed cells, full frame %d", step, diff.Len(), full.Len())
		}
		frame = changeCells(rng, frame, 10)
	}

	// An unchanged frame writes nothing at all.
	var same strings.Builder
	writeTextFrameRegion(&same, frame, top, left, 0, 0, cols, rows, &cache)
	writeTextFrameRegion(&same, frame, top, left, 0, 0, cols, rows, &cache)
	if s := same.String(); strings.Count(s, "\x1b[38;2") > 10*2 {
		t.Fatalf("unchanged frame redrawn: %d bytes", len(s))
	}
}

func TestPaneCacheRepaintsAfterADroppedFrame(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var cache screenCache
	now := time.Now()
	cache.prepare(40, 20, now)
	frame := randomTextFrame(rng, 10, 5)
	var first, again strings.Builder
	writeTextFrameRegion(&first, frame, 1, 1, 0, 0, 10, 5, &cache)
	screenDirty.Store(true) // the first frame never reached the terminal
	cache.prepare(40, 20, now)
	writeTextFrameRegion(&again, frame, 1, 1, 0, 0, 10, 5, &cache)
	if again.Len() != first.Len() {
		t.Fatalf("after a dropped frame: %d bytes, want the full %d", again.Len(), first.Len())
	}
	// And periodically, in case something else wrote over the panes.
	var periodic strings.Builder
	cache.prepare(40, 20, now.Add(screenRepaintEvery))
	writeTextFrameRegion(&periodic, frame, 1, 1, 0, 0, 10, 5, &cache)
	if periodic.Len() != first.Len() {
		t.Fatalf("periodic repaint: %d bytes, want the full %d", periodic.Len(), first.Len())
	}
}

// flatTextFrame is a frame of large single-colour areas with a few
// details: the cells around a change share colours, so short gaps are
// reprinted instead of skipped with cursor moves.
func flatTextFrame(cols, rows int) *codec.TextFrame {
	f := &codec.TextFrame{Cols: cols, Rows: rows, Glyphs: make([]byte, cols*rows),
		FgYCbCr: make([]byte, cols*rows*3), BgYCbCr: make([]byte, cols*rows*3)}
	for i := range f.Glyphs {
		f.Glyphs[i] = byte(i % 3) // includes the empty glyph
		copy(f.FgYCbCr[i*3:], []byte{200, 128, 128})
		copy(f.BgYCbCr[i*3:], []byte{40, 128, 128})
	}
	return f
}

func TestPaneDiffWithFlatAreasMatchesFullFrames(t *testing.T) {
	const cols, rows = 30, 10
	rng := rand.New(rand.NewSource(3))
	var cache screenCache
	cache.prepare(40, 15, time.Now())
	diffTerm := newVterm(40, 15)
	frame := flatTextFrame(cols, rows)
	for step := 0; step < 40; step++ {
		var diff, full strings.Builder
		writeTextFrameRegion(&diff, frame, 2, 3, 0, 0, cols, rows, &cache)
		writeTextFrameRegion(&full, frame, 2, 3, 0, 0, cols, rows, nil)
		diffTerm.apply(diff.String())
		fullTerm := newVterm(40, 15)
		fullTerm.apply(full.String())
		for i := range fullTerm.cells {
			if fullTerm.cells[i].set && diffTerm.cells[i] != fullTerm.cells[i] {
				t.Fatalf("step %d: cell %d: diff %+v, full %+v", step, i, diffTerm.cells[i], fullTerm.cells[i])
			}
		}
		// Change a few cells: glyph only, so colours around stay the same.
		g := *frame
		g.Glyphs = append([]byte(nil), frame.Glyphs...)
		for k := 0; k < 4; k++ {
			g.Glyphs[rng.Intn(len(g.Glyphs))] = byte(rng.Intn(len(glyphCodepoints)))
		}
		frame = &g
	}
}

// The whole call screen drawn incrementally over several frames looks
// exactly like the same screen drawn from scratch.
func TestChatScreenIncrementalMatchesFullRedraw(t *testing.T) {
	checkIncrementalMatchesFull(t)
}

// The same with the terminal's frames, whose cut corners the video leaves.
func TestFramedChatScreenIncrementalMatchesFullRedraw(t *testing.T) {
	setTermHUD(true)
	defer setTermHUD(false)
	checkIncrementalMatchesFull(t)
}

func checkIncrementalMatchesFull(t *testing.T) {
	const cols, rows = 120, 40
	vs, ok := calcViewportSlots(cols, rows)
	if !ok {
		t.Fatal("no layout")
	}
	SetVideoFPSLimit(0)
	rng := rand.New(rand.NewSource(4))
	local := randomTextFrame(rng, vs.selfSlot.cols, vs.selfSlot.rows)
	remote := randomTextFrame(rng, vs.peerSlot.cols, vs.peerSlot.rows)
	AddChatMessage("hello", true)
	AddChatMessage("привет, это ответ", false)

	paneCache = screenCache{}
	incremental := newVterm(cols, rows)
	for step := 0; step < 8; step++ {
		SetLocalFrame(local)
		SetRemoteFrame(remote)
		if step == 4 {
			AddChatMessage("new message", true)
		}
		incremental.apply(renderChatScreen(cols, rows))
		local, remote = changeCells(rng, local, 30), changeCells(rng, remote, 30)
	}
	SetLocalFrame(local)
	SetRemoteFrame(remote)
	incremental.apply(renderChatScreen(cols, rows))

	paneCache.reset()
	full := newVterm(cols, rows)
	full.apply(renderChatScreen(cols, rows))
	for i := range full.cells {
		row := i/cols + 1
		if row >= rows-termBorder-1 { // the input line's cursor blinks with time
			continue
		}
		if full.cells[i].set && incremental.cells[i] != full.cells[i] {
			t.Fatalf("row %d col %d: incremental %+v, full redraw %+v", row, i%cols+1, incremental.cells[i], full.cells[i])
		}
	}
}
