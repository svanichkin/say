package gui

import "sync"

// TrayItem is a line of the tray's menu: its title ("" a separator), its
// ID (handed back when chosen), whether it can be chosen, a dot before it
// (1 green: there, 2 red: not), the items under it (a submenu).
type TrayItem struct {
	Title string     `json:"t"`
	ID    int        `json:"id"`
	On    bool       `json:"on"`
	Dot   int        `json:"dot,omitempty"`
	Sub   []TrayItem `json:"sub,omitempty"`
}

var tray struct {
	sync.Mutex
	pick func(id int)
}
