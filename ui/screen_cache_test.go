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
			case 'm':
				switch {
				case params[0] == "0":
					fg, bg = [3]int{}, [3]int{}
				case len(params) == 5 && params[0] == "38":
					fg = [3]int{n(2), n(3), n(4)}
				case len(params) == 5 && params[0] == "48":
					bg = [3]int{n(2), n(3), n(4)}
				}
			}
			i = j + 1
			continue
		}
		r, size := []rune(s[i:])[0], len(string([]rune(s[i:])[0]))
		if row >= 1 && row <= t.rows && col >= 1 && col <= t.cols {
			t.cells[(row-1)*t.cols+col-1] = screenCell{r: r, fg: fg, bg: bg, set: true}
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
