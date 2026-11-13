package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/hud"
)

// With the HUD as an image the frames' cells stay blank: no frame
// characters, and the image goes once, again only when the scene changes
// or the screen was cleared.
func TestGraphicsHUDSendsTheFramesAsAnImage(t *testing.T) {
	setTermHUD(true)
	setGraphics(true)
	resetGfx := func() {
		gfx.mu.Lock()
		gfx.want, gfx.ready, gfx.shown, gfx.strips = gfxJob{}, gfxPNG{}, "", nil
		gfx.mu.Unlock()
	}
	resetGfx()
	defer func() { setGraphics(false); setTermHUD(false); resetGfx() }()
	defer func() {
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	cols, rows := 120, 44
	paneCache = screenCache{}
	out := renderChatScreen(cols, rows)
	v := newVterm(cols, rows)
	v.apply(out)
	blank := func(row, col int) bool { r := v.cells[(row-1)*cols+col-1].r; return r == ' ' || r == 0 }
	for r := 1; r <= rows; r++ { // the side bars
		for _, c := range []int{1, 2, cols - 1, cols} {
			if !blank(r, c) {
				t.Fatalf("a side bar's cell (%d, %d) is drawn in characters", r, c)
			}
		}
	}
	if len(gfxScene.Panes) != 3 || !gfxScene.Border || len(gfxScene.Brackets) != 1 {
		t.Fatalf("scene: %d panes, border %v, %d brackets; want 3, true, 1", len(gfxScene.Panes), gfxScene.Border, len(gfxScene.Brackets))
	}
	frames := fmt.Sprintf("i=%d,", gfxFramesID)
	strip := fmt.Sprintf("i=%d,", gfxFramesID+1)
	first := gfxOutput(cols, rows, strings.Contains(out, "\x1b[2J"))
	if !strings.Contains(first, strip) {
		t.Fatalf("the brackets' strip not sent: %.80q", first)
	}
	if strings.Contains(first, "\x1b[0m") {
		t.Fatal("the images' sequences carry an SGR reset")
	}
	// The frames come from another goroutine, with a later screen.
	deadline := time.Now().Add(10 * time.Second)
	for {
		gfx.mu.Lock()
		done := !gfx.busy && gfx.ready.key == gfx.want.key
		gfx.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the frames were never drawn")
		}
		time.Sleep(10 * time.Millisecond)
	}
	renderChatScreen(cols, rows)
	second := gfxOutput(cols, rows, false)
	if !strings.Contains(second, frames) || strings.Contains(second, strip) {
		t.Fatalf("the next screen: frames %v, strip %v; want the frames only", strings.Contains(second, frames), strings.Contains(second, strip))
	}
	renderChatScreen(cols, rows)
	if again := gfxOutput(cols, rows, false); again != "" {
		t.Fatalf("an unchanged screen sent images again (%d bytes)", len(again))
	}
	if cleared := gfxOutput(cols, rows, true); !strings.Contains(cleared, frames) || !strings.Contains(cleared, strip) {
		t.Fatal("not both sent again after the screen was cleared")
	}
	// The cut corners of the panes are left blank for the image; so are
	// the brackets' ends.
	for k := range gfxVideoMask(gfxGrid(cols, rows)).cells {
		if !blank(k[0], k[1]) {
			t.Fatalf("cell %v, outside its pane's frame, is drawn", k)
		}
	}
	b := gfxScene.Brackets[0]
	if !blank(b.Row+1, b.Col+1) || !blank(b.Row+1, b.Col+b.Cols) {
		t.Fatal("a bracket's end is drawn in characters")
	}
}

func TestKittyPlaceChunks(t *testing.T) {
	data := make([]byte, 10000) // over two chunks of base64
	s := kittyPlace(1, data, 1, 1, 80, 24)
	if n := strings.Count(s, "\x1b_G"); n != 4 {
		t.Fatalf("%d escape sequences for 10000 bytes, want 4", n)
	}
	if !strings.Contains(s, "c=80,r=24,m=1;") || !strings.Contains(s, "\x1b_Gm=0;") {
		t.Fatalf("chunking: %.120q", s)
	}
}

func TestInsidePolygon(t *testing.T) {
	// A 100 x 100 square with its top left corner cut 30 deep.
	poly := []hud.Pt{{0, 30}, {30, 0}, {100, 0}, {100, 100}, {0, 100}}
	for _, c := range []struct {
		x, y float64
		in   bool
	}{{50, 50, true}, {5, 5, false}, {20, 20, true}, {14, 14, false}, {150, 50, false}} {
		if got := insidePolygon(poly, c.x, c.y); got != c.in {
			t.Fatalf("(%v, %v) inside = %v, want %v", c.x, c.y, got, c.in)
		}
	}
}
