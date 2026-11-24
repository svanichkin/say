package ui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
)

// A click on the self view opens a menu in its place: the microphone and the
// camera, each on (a green tick) or off (a red cross), and, where the system
// has them, its own microphone settings (an arrow: it opens something else).
// A click on the peer's view opens its menu: its sound and its video (not
// shown: its pane loses the signal, the noise then the test card). A click on an item does it and closes the
// menu; a click anywhere else only closes it. What is off shows under the
// pane in place of its rate (AMUTE, VMUTE).

const (
	menuOn    = "✅"
	menuOff   = "❌"
	menuOpens = "➡️"
)

var selfMenu struct {
	sync.Mutex
	open  bool
	peer  bool      // the peer view's menu, not ours
	items []menuHit // where the last drawn menu's items are
}

// menuHit is an item's row (1-based cells) across the pane.
type menuHit struct {
	row, left, cols int
	do              func()
}

type menuItem struct {
	icon, label string
	iconColour  [3]int
	do          func()
}

// menuOpen reports whether a menu is open (ours or the peer view's).
func menuOpen() bool {
	selfMenu.Lock()
	defer selfMenu.Unlock()
	return selfMenu.open
}

func selfMenuOpen() bool {
	selfMenu.Lock()
	defer selfMenu.Unlock()
	return selfMenu.open && !selfMenu.peer
}

func peerMenuOpen() bool {
	selfMenu.Lock()
	defer selfMenu.Unlock()
	return selfMenu.open && selfMenu.peer
}

// setSelfMenu opens our view's menu, or closes whichever is open.
func setSelfMenu(open bool) { setMenu(open, false) }

// setPeerMenu opens the peer view's menu, or closes whichever is open.
func setPeerMenu(open bool) { setMenu(open, true) }

func setMenu(open, peer bool) {
	selfMenu.Lock()
	selfMenu.open, selfMenu.peer = open, peer && open
	if !open {
		selfMenu.items = nil
	}
	selfMenu.Unlock()
}

// selfMenuClick handles a click while the menu is open: the item under it
// is done, and the menu closes either way.
func selfMenuClick(col, row int) {
	selfMenu.Lock()
	var do func()
	for _, it := range selfMenu.items {
		if row == it.row && col >= it.left && col < it.left+it.cols {
			do = it.do
		}
	}
	selfMenu.open, selfMenu.items = false, nil
	selfMenu.Unlock()
	if do != nil {
		do()
	}
}

func selfMenuItems() []menuItem {
	state := func(on bool) (string, [3]int) {
		if on {
			return menuOn, [3]int{40, 200, 70}
		}
		return menuOff, [3]int{230, 40, 40}
	}
	micIcon, micColour := state(mediactrl.CaptureEnabled())
	camIcon, camColour := state(mediactrl.VideoEnabled())
	items := []menuItem{
		{micIcon, lang.T(lang.MenuMic), micColour, func() {
			if mediactrl.ToggleCapture() {
				logs.LogV("[ui] microphone on (menu)")
			} else {
				logs.LogV("[ui] microphone off (menu)")
			}
		}},
		{camIcon, lang.T(lang.MenuCam), camColour, func() {
			if mediactrl.ToggleVideo() {
				logs.LogV("[ui] camera on (menu)")
			} else {
				logs.LogV("[ui] camera off (menu)")
				ClearLocalFrame() // the self view loses the signal now
			}
		}},
	}
	settings := map[device.MicSettingsKind]lang.Key{device.MicSettingsMode: lang.MicMode, device.MicSettingsPrivacy: lang.MicPrivacy}
	if key, ok := settings[device.MicSettings()]; ok {
		items = append(items, menuItem{menuOpens, lang.T(key), [3]int{255, 255, 255}, func() {
			if err := device.OpenMicSettings(); err != nil {
				logs.LogV("[ui] microphone settings: %v", err)
			}
		}})
	}
	return items
}

// peerMenuItems are the peer view's: its sound (played or not) and its
// video (shown or not).
func peerMenuItems() []menuItem {
	state := func(on bool) (string, [3]int) {
		if on {
			return menuOn, [3]int{40, 200, 70}
		}
		return menuOff, [3]int{230, 40, 40}
	}
	soundIcon, soundColour := state(mediactrl.PlaybackEnabled())
	videoIcon, videoColour := state(mediactrl.PeerVideoShown())
	return []menuItem{
		{soundIcon, lang.T(lang.MenuPeerSound), soundColour, func() {
			if mediactrl.TogglePlayback() {
				logs.LogV("[ui] remote playback resumed (menu)")
			} else {
				logs.LogV("[ui] remote playback paused (menu)")
			}
		}},
		{videoIcon, lang.T(lang.MenuPeerVideo), videoColour, func() {
			if mediactrl.TogglePeerVideo() {
				logs.LogV("[ui] remote video shown (menu)")
			} else {
				logs.LogV("[ui] remote video hidden (menu)")
			}
		}},
	}
}

// writeSelfMenu draws our menu over the self pane (1-based top, left), in
// place of its video, and keeps where its items are for clicks.
func writeSelfMenu(out *strings.Builder, top, left int, slot slotSize) {
	self, _ := Accents()
	writeMenu(out, top, left, slot, selfMenuItems(), self, false)
}

// writePeerMenu draws the peer view's menu over its pane.
func writePeerMenu(out *strings.Builder, top, left int, slot slotSize) {
	_, peer := Accents()
	writeMenu(out, top, left, slot, peerMenuItems(), peer, true)
}

// writeMenu draws items over a pane in place of its video, its labels in accent (the window's colours), the peer's pane
// mirrored in the window as its frame is.
func writeMenu(out *strings.Builder, top, left int, slot slotSize, items []menuItem, accent [3]int, peer bool) {
	if slot.cols <= 0 || slot.rows <= 0 {
		return
	}
	if pixelMode {
		// The window keeps the pane (its frame) with no video under it.
		writePixelPane(out, nil, top, left, slot.cols, slot.rows, peer)
	}
	paneCache.invalidate(top, left, slot.cols, slot.rows)
	bg := panelColors()
	text := bg
	if framed() {
		text += accentSGR(accent)
	}
	// An item: its icon (two cells), a space, the label. Each is placed
	// on its own, as terminals disagree on how wide an emoji is.
	width := 0
	for _, it := range items {
		width = max(width, 3+displayWidth(it.label))
	}
	width = min(width, slot.cols)
	step := 1
	if slot.rows >= 2*len(items)-1 {
		step = 2 // a blank row between items
	}
	first := top + max(0, (slot.rows-(len(items)-1)*step-1)/2)
	col := left + max(0, (slot.cols-width)/2)
	blank := strings.Repeat(" ", slot.cols)
	for r := 0; r < slot.rows; r++ {
		fmt.Fprintf(out, "\x1b[%d;%dH%s%s", top+r, left, bg, blank)
	}
	var hits []menuHit
	for i, it := range items {
		row := first + i*step
		if row >= top+slot.rows {
			break
		}
		fmt.Fprintf(out, "\x1b[%d;%dH%s%s%s", row, col, bg, iconSGR(it.iconColour), it.icon)
		if label := truncateWidth(it.label, width-3); label != "" {
			fmt.Fprintf(out, "\x1b[%d;%dH%s%s", row, col+3, text, label)
		}
		hits = append(hits, menuHit{row: row, left: left, cols: slot.cols, do: it.do})
	}
	out.WriteString("\x1b[0m")
	selfMenu.Lock()
	if selfMenu.open {
		selfMenu.items = hits
	}
	selfMenu.Unlock()
}

// iconSGR colours an icon where the terminal draws emoji as text (the window,
// a terminal without colour emoji): green, red, white.
func iconSGR(c [3]int) string {
	if ansiColors() {
		switch {
		case c[1] > c[0]:
			return "\x1b[1;32m"
		case c[0] > c[1]:
			return "\x1b[1;31m"
		}
		return "\x1b[1;37m"
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
}
