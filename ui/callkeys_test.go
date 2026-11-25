package ui

import (
	"testing"
	"time"

	"github.com/svanichkin/say/keys"
	"github.com/svanichkin/say/mediactrl"
)

// The arrows choose a video and work its menu: → opens the peer's, ← ours,
// ↓ chooses the camera, Enter switches it off and closes the menu; typing
// is the chat's and closes a menu.
func TestCallKeys(t *testing.T) {
	SetChatActions(func(string) {}, func(string) {})
	defer func() {
		SetChatActions(nil, nil)
		setSelfMenu(false)
		if !mediactrl.VideoEnabled() {
			mediactrl.ToggleVideo()
		}
		updateIdlePatternSlot(localFrameType, false, time.Now())
		updateIdlePatternSlot(remoteFrameType, false, time.Now())
		updateNoiseAnimation(false)
	}()
	cols, rows := 120, 40
	broadcastTermSize(cols, rows)
	callKey(keys.Right)
	if !peerMenuOpen() {
		t.Fatal("→ did not open the peer's menu")
	}
	callKey(keys.Left)
	if !selfMenuOpen() {
		t.Fatal("← did not open our menu")
	}
	renderChatScreen(cols, rows) // its items, where they are
	callKey(keys.Down)
	if menuChosen() != 1 {
		t.Fatalf("↓ chose item %d", menuChosen())
	}
	callKey('\r')
	if menuOpen() || mediactrl.VideoEnabled() {
		t.Fatalf("Enter on the camera: menu open %v, camera on %v", menuOpen(), mediactrl.VideoEnabled())
	}
	if callKey('\r') {
		t.Fatal("Enter with no menu is not the chat's")
	}
	callKey(keys.Right)
	if callKey('a') || menuOpen() {
		t.Fatal("typing is not the chat's, or the menu stayed")
	}
}
