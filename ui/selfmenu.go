package ui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
)

// A click on the self view opens a menu in its place: the microphone and the
// camera, each on (a green tick) or off (a red cross), and hanging up (x, as
// every menu's x closes it). The marks are the home
// screen's characters, a cell each, never emoji.
// A click on the peer's view opens its menu: its sound and its video (not
// shown: its pane loses the signal, the noise then the test card). A click on an item does it and closes the
// menu; a click anywhere else only closes it. What is off shows under the
// pane in place of its rate (AMUTE, VMUTE).

const (
	menuOn  = "✓"
	menuOff = "x"
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
	close       bool // x: hanging up, under the line as every menu's x
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
	if open { // its first item chosen
		callNav.Lock()
		callNav.sel = 0
		callNav.Unlock()
	}
}

// menuDo does the open menu's item i (Enter), as a click on it: the menu
// closes either way.
func menuDo(i int) {
	selfMenu.Lock()
	var do func()
	if i >= 0 && i < len(selfMenu.items) {
		do = selfMenu.items[i].do
	}
	selfMenu.open, selfMenu.items = false, nil
	selfMenu.Unlock()
	if do != nil {
		do()
	}
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
		}, false},
		{camIcon, lang.T(lang.MenuCam), camColour, func() {
			if mediactrl.ToggleVideo() {
				logs.LogV("[ui] camera on (menu)")
			} else {
				logs.LogV("[ui] camera off (menu)")
				ClearLocalFrame() // the self view loses the signal now
			}
		}, false},
	}
	if hang := hangupFunc.get(); hang != nil {
		items = append(items, menuItem{"", "", [3]int{}, func() {
			logs.LogV("[ui] hang up (menu)")
			go hang()
		}, true})
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
		}, false},
		{videoIcon, lang.T(lang.MenuPeerVideo), videoColour, func() {
			if mediactrl.TogglePeerVideo() {
				logs.LogV("[ui] remote video shown (menu)")
			} else {
				logs.LogV("[ui] remote video hidden (menu)")
			}
		}, false},
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

// writeMenu draws items over a pane in place of its video, by the menus'
// rules (CLAUDE.md, «Интерфейс»): the pane's own frame is the menu's (no
// frame in a frame); the items centred across it, a cell of padding at
// least each side, a row above and below, the rows centred when it is
// taller; the chosen item (the arrows move it) on a block of the view's
// colour from side to side; each item's mark before it; hanging up (x, in
// red, as every menu's) under a line across, joined to the frame's sides. It keeps where the items are
// for clicks.
func writeMenu(out *strings.Builder, top, left int, slot slotSize, items []menuItem, accent [3]int, peer bool) {
	if slot.cols <= 0 || slot.rows <= 0 || len(items) == 0 {
		return
	}
	col := make([]HomeItem, len(items))
	for i, it := range items {
		col[i] = HomeItem{Key: it.label, Text: it.label, Mark: it.icon, MarkColour: it.iconColour, Close: it.close}
	}
	cur := min(max(0, menuChosen()), len(col)-1)
	right, bottom := left+slot.cols-1, top+slot.rows-1
	n := homeSlot(col, len(col)-1) + 1 // rows, the line's among them in a terminal
	first := top + max(min(homePadRows(), max(0, slot.rows-n)), (slot.rows-n)/2)
	sepRow := 0
	if sep := navFrom(col); sep >= 0 {
		sepRow = first + homeSlot(col, sep)
		if !pixelMode {
			sepRow-- // the line's own row, above
		}
	}
	if pixelMode {
		// The window keeps the pane (its frame) with no video under it,
		// and draws the line across it, along the top of hanging up.
		divider := 0
		if sepRow > 0 {
			divider = sepRow - top + 1
		}
		writePixelPane(out, nil, top, left, slot.cols, slot.rows, peer, divider)
	}
	paneCache.invalidate(top, left, slot.cols, slot.rows)
	blank := strings.Repeat(" ", slot.cols)
	for r := 0; r < slot.rows; r++ {
		fmt.Fprintf(out, "\x1b[%d;%dH\x1b[0m%s", top+r, left, blank)
	}
	if sepRow > 0 && !pixelMode && sepRow <= bottom {
		homePut(out, sepRow, left, right, strings.Repeat("─", slot.cols), homeFg(accent))
		if hudVideoMask != nil { // joined to the frame's sides (├ ┤ by their weight)
			hudVideoMask.tee(sepRow, left-1, true, accent)
			hudVideoMask.tee(sepRow, right+1, false, accent)
		}
	}
	var hits []menuHit
	for i, it := range col {
		row := first + homeSlot(col, i)
		if row > bottom {
			break
		}
		text, style := homeLine(it, i == cur, true, accent)
		text = truncateWidth(text, max(1, slot.cols-2*homePadCols()))
		x := left + (slot.cols-displayWidth(text))/2
		if i == cur {
			homePut(out, row, left, right, blank, homeChosen(accent))
		}
		homePut(out, row, x, right, text, style)
		putTick(out, it, text, row, x, right, style, i == cur)
		hits = append(hits, menuHit{row: row, left: left, cols: slot.cols, do: items[i].do})
	}
	out.WriteString("\x1b[0m")
	selfMenu.Lock()
	if selfMenu.open {
		selfMenu.items = hits
	}
	selfMenu.Unlock()
}

// iconSGR colours a mark: green, red, white.
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

// The captions' switches: where the last drawn captions' sound and video
// parts are (captionHits), for clicks.
var captionSwitches struct {
	sync.Mutex
	hits []menuHit
}

func setCaptionHits(hits []menuHit) {
	captionSwitches.Lock()
	captionSwitches.hits = hits
	captionSwitches.Unlock()
}

// captionHits adds a caption's sound part a and video part v (as written in
// caption at span, its columns on row) with what a click on each does.
func captionHits(hits []menuHit, row int, span [2]int, caption, a, v string, doA, doV func()) []menuHit {
	caption = sanitizeLine(caption)
	off := strings.LastIndex(caption, a+" "+v)
	if off < 0 || span[1] <= span[0] {
		return hits
	}
	ac := span[0] + displayWidth(caption[:off])
	vc := ac + displayWidth(a) + 1
	if vc+displayWidth(v) > span[1] {
		return hits // cut short: not all of it shown
	}
	return append(hits, menuHit{row: row, left: ac, cols: displayWidth(a), do: doA},
		menuHit{row: row, left: vc, cols: displayWidth(v), do: doV})
}

// captionClick does the switch under a click on a caption (false: none).
func captionClick(col, row int) bool {
	captionSwitches.Lock()
	var do func()
	for _, h := range captionSwitches.hits {
		if row == h.row && col >= h.left && col < h.left+h.cols {
			do = h.do
		}
	}
	captionSwitches.Unlock()
	if do == nil {
		return false
	}
	do()
	return true
}
