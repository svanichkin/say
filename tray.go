package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/contacts"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/gui"
	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/ui"
)

// Say's icon: in the Dock (as any app) or in the tray (the menu bar's,
// no Dock icon), chosen in the settings and kept beside the configuration
// (config.json: config-tray.json). The tray's menu is the contacts, a
// green or red ● before each, each opening to its devices to call (one not
// there, or this one, not callable), our address (chosen: copied), then
// Open Say and Quit.

// trayState is where the choice is kept, whether it is on, and what the
// menu's IDs do.
type trayState struct {
	mu      sync.Mutex
	path    string
	on      bool
	actions map[int]func()
	quit    func()
}

// trayPath is the file keeping the choice ("" none: no configuration).
func trayPath(args []string) string {
	cfg := conf.ConfigPathFromArgs(args)
	if cfg == "" {
		return ""
	}
	return strings.TrimSuffix(cfg, filepath.Ext(cfg)) + "-tray.json"
}

// initTray reads the choice and, in the tray, puts the icon there.
func (a *homeApp) initTray(path string, quit func()) {
	a.tray.mu.Lock()
	a.tray.path, a.tray.quit = path, quit
	if data, err := os.ReadFile(path); err == nil {
		var v struct{ Tray bool }
		if json.Unmarshal(data, &v) == nil {
			a.tray.on = v.Tray
		}
	}
	a.tray.mu.Unlock()
	a.refreshTray()
}

// trayOn reports whether Say lives in the tray.
func (a *homeApp) trayOn() bool {
	a.tray.mu.Lock()
	defer a.tray.mu.Unlock()
	return a.tray.on
}

// setTray moves the icon (true: to the tray) and keeps the choice.
func (a *homeApp) setTray(on bool) {
	a.tray.mu.Lock()
	a.tray.on = on
	path := a.tray.path
	a.tray.mu.Unlock()
	if path != "" {
		data, _ := json.Marshal(struct{ Tray bool }{on})
		if err := os.WriteFile(path, data, 0o644); err != nil {
			log.Printf("[gui] the tray choice not kept: %v", err)
		}
	}
	if !on {
		gui.SetTray(false, nil, nil)
		return
	}
	a.refreshTray()
}

// refreshTray builds the tray's menu again (the contacts or who is there
// changed); nothing when Say is in the Dock.
func (a *homeApp) refreshTray() {
	if !gui.TrayAvailable() || !ui.PixelMode() || !a.trayOn() {
		return
	}
	sn := a.store.Snapshot()
	actions := map[int]func(){}
	next := 0
	add := func(f func()) int {
		next++
		actions[next] = f
		return next
	}
	dot := func(online, offline bool) int {
		switch {
		case online:
			return 1
		case offline:
			return 2
		}
		return 0
	}
	var items []gui.TrayItem
	for _, p := range sortedPeople(sn, sn.People) {
		var sub []gui.TrayItem
		for _, d := range p.Devices {
			d := d
			callable := d.Host != sn.Self && !a.deviceOffline(d.Host)
			sub = append(sub, gui.TrayItem{
				Title: lang.T(lang.HomeCallOn, labelText(d)), On: callable,
				Dot: dot(a.deviceOnline(d.Host), d.Host != sn.Self && !a.deviceOnline(d.Host)),
				ID:  add(func() { gui.ShowWindow(); a.call(d) }),
			})
		}
		items = append(items, gui.TrayItem{Title: p.Display(sn.Display), On: len(sub) > 0,
			Dot: dot(a.personOnline(p, sn.Self), a.personOffline(p, sn.Self)), Sub: sub})
	}
	if len(items) > 0 {
		items = append(items, gui.TrayItem{}) // a separator
	}
	a.tray.mu.Lock()
	quit := a.tray.quit
	a.tray.mu.Unlock()
	// Our address, as in the home menu's "My address": chosen, it is copied.
	if sn.Self != "" {
		addr := contacts.Device{Host: sn.Self, Port: sn.Port}.Shown()
		items = append(items, gui.TrayItem{Title: lang.T(lang.HomeMyAddress), On: true, Sub: []gui.TrayItem{
			{Title: addr, On: true, ID: add(func() { device.CopyToClipboard(addr) })},
		}}, gui.TrayItem{})
	}
	items = append(items,
		gui.TrayItem{Title: lang.T(lang.TrayOpen), On: true, ID: add(gui.ShowWindow)},
		gui.TrayItem{},
		gui.TrayItem{Title: lang.T(lang.TrayQuit), On: true, ID: add(func() {
			if quit != nil {
				quit()
			}
		})})
	a.tray.mu.Lock()
	a.tray.actions = actions
	a.tray.mu.Unlock()
	gui.SetTray(true, items, a.trayPick)
}

// trayPick does what a menu item chosen does.
func (a *homeApp) trayPick(id int) {
	a.tray.mu.Lock()
	f := a.tray.actions[id]
	a.tray.mu.Unlock()
	if f != nil {
		f()
	}
}

// iconItem is the settings' choice of where Say's icon lives (where the
// tray is to be had, in the window).
func (a *homeApp) iconItem() (ui.HomeItem, bool) {
	if !gui.TrayAvailable() || !ui.PixelMode() {
		return ui.HomeItem{}, false
	}
	return ui.HomeItem{Key: "icon", Text: lang.T(lang.HomeIcon), Open: func() []ui.HomeItem {
		on := a.trayOn()
		return []ui.HomeItem{
			{Key: "dock", Text: lang.T(lang.HomeInDock), Tick: !on, Start: !on, Do: func() { a.setTray(false) }},
			{Key: "tray", Text: lang.T(lang.HomeInTray), Tick: on, Start: on, Do: func() { a.setTray(true) }},
			back(), closeItem(),
		}
	}}, true
}
