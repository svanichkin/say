package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/mediactrl"
)

// A click on the self view opens the menu in its place; an item switches
// the camera off and closes the menu, and the caption says what is off. A
// click on the peer's view switches its sound at once.
func TestSelfMenu(t *testing.T) {
	SetChatActions(func(string) {}, func(string) {})
	defer func() {
		SetChatActions(nil, nil)
		setSelfMenu(false)
		if !mediactrl.VideoEnabled() {
			mediactrl.ToggleVideo()
		}
		mediactrl.SetPlaybackEnabled(true)
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	cols, rows := 120, 40
	broadcastTermSize(cols, rows)
	vs, ok := calcViewportSlots(cols, rows)
	if !ok || vs.orient != 'H' {
		t.Fatal("no side by side layout")
	}
	canvasLeft, canvasCols, top := termSide+1, cols-2*termSide, termBorder+1
	videoLeft := canvasLeft + max(0, (canvasCols-(vs.selfSlot.cols+termGap+vs.peerSlot.cols))/2)
	peerLeft := videoLeft + vs.selfSlot.cols + termGap

	ClickAt(videoLeft+1, top+1)
	if !selfMenuOpen() {
		t.Fatal("a click on the self view opened no menu")
	}
	screen := renderChatScreen(cols, rows)
	for _, want := range []string{lang.T(lang.MenuMic), lang.T(lang.MenuCam), menuOn} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the menu has no %q", want)
		}
	}
	selfMenu.Lock()
	hits := append([]menuHit(nil), selfMenu.items...)
	selfMenu.Unlock()
	if len(hits) < 2 {
		t.Fatalf("%d items to click", len(hits))
	}
	ClickAt(hits[1].left+hits[1].cols/2, hits[1].row)
	if selfMenuOpen() || mediactrl.VideoEnabled() {
		t.Fatalf("the camera item: menu open %v, camera on %v", selfMenuOpen(), mediactrl.VideoEnabled())
	}
	screen = renderChatScreen(cols, rows)
	if !strings.Contains(screen, "V MUTE") || strings.Contains(screen, lang.T(lang.MenuCam)) {
		t.Fatal("no V MUTE under the pane, or the menu stayed")
	}

	ClickAt(videoLeft+1, top+1)
	if screen = renderChatScreen(cols, rows); !strings.Contains(screen, menuOff) {
		t.Fatal("the camera off is not crossed in the menu")
	}
	ClickAt(1, 1) // not an item
	if selfMenuOpen() || mediactrl.VideoEnabled() {
		t.Fatal("a click past the items did not only close the menu")
	}

	ClickAt(peerLeft+1, top+1)
	if mediactrl.PlaybackEnabled() || selfMenuOpen() {
		t.Fatal("a click on the peer's view did not mute its sound at once")
	}
	if screen = renderChatScreen(cols, rows); strings.Count(screen, "A MUTE") != 1 {
		t.Fatal("no A MUTE under the peer's pane")
	}
}
