package ui

import (
	"sync"

	"github.com/svanichkin/say/keys"
)

// Keys on the call's screen: the arrows choose a video and work its menu.
// ← opens our view's menu in its place, → the peer's (from one menu to the
// other too); ↑ and ↓ choose an item, Enter does it, Esc closes the menu.
// Whatever is typed goes to the chat (closing a menu); Enter there sends.

var callNav struct {
	sync.Mutex
	sel int // the open menu's chosen item
}

// menuChosen is the open menu's chosen item.
func menuChosen() int {
	callNav.Lock()
	defer callNav.Unlock()
	return callNav.sel
}

// callKey handles a key on the call's screen; false: it is the chat's
// (text, or Enter to send).
func callKey(r rune) bool {
	switch r {
	case keys.Left:
		if !selfMenuOpen() {
			setSelfMenu(true)
		}
	case keys.Right:
		if !peerMenuOpen() {
			setPeerMenu(true)
		}
	case keys.Up, keys.Down:
		if !menuOpen() {
			return true
		}
		selfMenu.Lock()
		n := len(selfMenu.items)
		selfMenu.Unlock()
		callNav.Lock()
		if r == keys.Up {
			callNav.sel = max(0, callNav.sel-1)
		} else {
			callNav.sel = min(max(0, n-1), callNav.sel+1)
		}
		callNav.Unlock()
	case '\r', '\n':
		if !menuOpen() {
			return false // sent
		}
		menuDo(menuChosen())
	case keys.Esc:
		setSelfMenu(false)
	default:
		if keys.Special(r) {
			return true
		}
		setSelfMenu(false) // typing: the chat's
		return false
	}
	RequestRedraw()
	return true
}
