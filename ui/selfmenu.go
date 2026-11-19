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
// A click on an item does it and closes the menu; a click anywhere else only
// closes it. What is off shows under the pane (A MUTE, V MUTE).

const (
	menuOn    = "✅"
	menuOff   = "❌"
	menuOpens = "➡️"
)

var selfMenu struct {
	sync.Mutex
	open  bool
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

func selfMenuOpen() bool {
	selfMenu.Lock()
	defer selfMenu.Unlock()
	return selfMenu.open
}

func setSelfMenu(open bool) {
	selfMenu.Lock()
	selfMenu.open = open
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

// writeSelfMenu draws the menu over the self pane (1-based top, left), in
// place of its video, and keeps where its items are for clicks.
func writeSelfMenu(out *strings.Builder, top, left int, slot slotSize) {
	if slot.cols <= 0 || slot.rows <= 0 {
		return
	}
	if pixelMode {
		// The window keeps the pane (its frame) with no video under it.
		writePixelPane(out, nil, top, left, slot.cols, slot.rows, false)
	}
	paneCache.invalidate(top, left, slot.cols, slot.rows)
	items := selfMenuItems()
	bg := panelColors()
	text := bg
	if framed() {
		self, _ := Accents()
		text += accentSGR(self)
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

// muteCaption is what is off on a side, before its caption: the sound (A),
// our camera (V).
func muteCaption(audioOn, videoOn bool) string {
	var s []string
	if !audioOn {
		s = append(s, "A MUTE")
	}
	if !videoOn {
		s = append(s, "V MUTE")
	}
	if len(s) == 0 {
		return ""
	}
	return strings.Join(s, " ") + " "
}
