//go:build darwin

package gui

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void sayTraySet(int on, const char *json);
void sayTraySetIcon(const void *png, int n);
int sayQuitting(void);
void sayCatchQuit(void);
*/
import "C"

import (
	"encoding/json"
	"sync"
	"unsafe"
)

// trayIconOnce gives the tray its icon, Say's bubble, once.
var trayIconOnce sync.Once

// TrayAvailable reports whether Say can live in the tray here.
func TrayAvailable() bool { return true }

// SetTray puts Say's icon in the menu bar with items as its menu, no Dock
// icon (on false: back in the Dock, no icon in the bar); pick gets the ID
// of an item chosen. Called again, it changes the menu.
func SetTray(on bool, items []TrayItem, pick func(id int)) {
	tray.Lock()
	tray.pick = pick
	tray.Unlock()
	trayIconOnce.Do(func() {
		if data, err := iconFiles.ReadFile("icon/tray.png"); err == nil && len(data) > 0 {
			C.sayTraySetIcon(unsafe.Pointer(&data[0]), C.int(len(data)))
		}
	})
	var cs *C.char
	if on {
		data, _ := json.Marshal(items)
		cs = C.CString(string(data))
		defer C.free(unsafe.Pointer(cs))
	}
	on1 := C.int(0)
	if on {
		on1 = 1
	}
	C.sayTraySet(on1, cs)
}

//export sayTrayPick
func sayTrayPick(id C.int) {
	tray.Lock()
	pick := tray.pick
	tray.Unlock()
	if pick != nil {
		go pick(int(id))
	}
}

// catchQuit tells Quit (the Dock's, the app menu's) from closing the
// window: called once the window runs.
func catchQuit() { C.sayCatchQuit() }

// quitAsked reports whether Quit was chosen (not the close button).
func quitAsked() bool { return C.sayQuitting() != 0 }
