package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/svanichkin/say/contacts"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/logs"
	tcpsignal "github.com/svanichkin/say/network/tcp"
	"github.com/svanichkin/say/tones"
	"github.com/svanichkin/say/ui"
)

// homeApp is the home screen's side in main: the menu over the address
// book (contacts.Store: it reads and writes, the menu draws its snapshots
// and asks it for changes), and the calls made from the menu.
type homeApp struct {
	store *contacts.Store

	mu      sync.Mutex
	dial    func(ctx context.Context, d contacts.Device) error // calls a device; returns when the call is over
	calling context.CancelFunc

	browsed map[string][]string // the folder chooser's folders, read once while it is open

	online map[string]bool // the devices whose Say answered last time, by host (presence.go)
	asked  map[string]bool // the devices asked last time
	tray   trayState       // Say's icon in the tray, its menu (tray.go)
	// presenceKick asks now, not at the next round (askPresenceSoon).
	presenceKick chan struct{}

	// The forms' drafts, by form: they live while the form is open (it is
	// built again after every change), a new one each time it is entered.
	drafts map[string]*draft
	blocks map[string][]*contacts.Device // an edited card's devices being added
}

// draftFor is the form's draft (made the first time).
func (a *homeApp) draftFor(form string) *draft {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.drafts == nil {
		a.drafts = map[string]*draft{}
	}
	dr := a.drafts[form]
	if dr == nil {
		dr = &draft{}
		a.drafts[form] = dr
	}
	return dr
}

// newDraft starts the form afresh: entering it, and leaving it (made or
// not, what was typed does not stay to be shown).
func (a *homeApp) newDraft(form string) func() {
	return func() {
		a.mu.Lock()
		delete(a.drafts, form)
		delete(a.blocks, form)
		a.mu.Unlock()
	}
}

func newHomeApp(configPath, contactsDir, self string, port int) *homeApp {
	a := &homeApp{presenceKick: make(chan struct{}, 1)}
	a.store = contacts.NewStore(contacts.SettingsPath(configPath), expandHome(contactsDir), kindLabel(), port, func() {
		ui.HomeRefresh()
		a.refreshTray()
		a.askPresenceSoon() // the contacts came or changed: who of them is there
	})
	if self != "" {
		a.store.SetSelf(self)
	}
	return a
}

// kindLabel is this device's label by its kind.
func kindLabel() string {
	if device.Kind() == "laptop" {
		return lang.T(lang.HomeLaptop)
	}
	return lang.T(lang.HomeComputer)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// setSelf is our address, once the node has it: it goes on our card.
func (a *homeApp) setSelf(addr string) { a.store.SetSelf(addr) }

// setDial is how calls are made, once the node is up and listening.
func (a *homeApp) setDial(dial func(ctx context.Context, d contacts.Device) error) {
	a.mu.Lock()
	a.dial = dial
	a.mu.Unlock()
}

// nameOf is a caller's name in the address book ("": not there).
func (a *homeApp) nameOf(host string) string { return a.store.NameOf(host) }

// noted records a call with host: its person comes up first next time.
func (a *homeApp) noted(host string) { a.store.Noted(host) }

// lookup is the address of a person (by name) for `say <name>`.
func (a *homeApp) lookup(name string) (contacts.Device, bool) { return a.store.Lookup(name) }

// call dials a device from the menu: the status screen while it rings,
// the call, then the home screen again.
func (a *homeApp) call(d contacts.Device) {
	a.mu.Lock()
	dial := a.dial
	a.mu.Unlock()
	if dial != nil { // nil: the node not up yet
		a.place(d, dial)
	}
}

// hangup ends the call on: ours (its redials too) or a caller's. The home
// screen comes back when our dial returns.
func (a *homeApp) hangup() {
	a.mu.Lock()
	cancel := a.calling
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	tcpsignal.HangUp()
}

// place makes a call with dial (the menu's few tries, or the command
// line's redials until hung up), then shows the home screen again.
func (a *homeApp) place(d contacts.Device, dial func(ctx context.Context, d contacts.Device) error) {
	a.mu.Lock()
	if a.calling != nil {
		a.mu.Unlock()
		return // a call on
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.calling = cancel
	a.mu.Unlock()
	ui.ShowHome(false)
	ui.SetDialCancel(cancel)
	go func() {
		err := dial(ctx, d)
		ui.SetDialCancel(nil)
		a.mu.Lock()
		a.calling = nil
		a.mu.Unlock()
		cancel()
		if failed(err) { // busy, declined, unanswered: the fail sound, home at once
			device.PlayTone(tones.Fail(), tones.Rate, false)
		}
		if err != nil && !errors.Is(err, context.Canceled) && !failed(err) {
			log.Printf("[p2p] call to %s: %v", d.Addr(), err)
			time.Sleep(2 * time.Second) // the reason stays on the screen a moment
		}
		ui.HomeRefresh()
		ui.ShowHome(true)
	}()
}

// paste is an address pasted on the home screen: it is called.
func (a *homeApp) paste(s string) {
	host, port, err := contacts.ParseAddr(s)
	if err != nil {
		return
	}
	a.call(contacts.Device{Host: host, Port: port})
}

// ---- the menu ----

func back() ui.HomeItem      { return ui.HomeItem{Key: "<", Back: true} }
func closeItem() ui.HomeItem { return ui.HomeItem{Key: "x", Close: true} }

// root is the first column: the contacts, an address to call, ours (to
// copy), the settings, quit. The contacts
// are where they were chosen, else the system's book or the usual folder
// (contacts.DefaultFolder); the system asks for its book the first time
// the contacts are opened.
func (a *homeApp) root() []ui.HomeItem {
	return []ui.HomeItem{
		{Key: "contacts", Text: lang.T(lang.HomeContacts), Start: true, Open: a.contactsMenu, Enter: func() {
			if a.store.Snapshot().NeedAccess {
				go a.store.AskAccess()
			}
		}},
		// An address typed and Enter: called (not an address: it blinks).
		{Key: "dial", Field: &ui.HomeField{Placeholder: lang.T(lang.HomeDial), Submit: a.dialTyped}},
		{Key: "myaddr", Text: lang.T(lang.HomeMyAddress), Open: a.myAddressMenu},
		{Key: "settings", Text: lang.T(lang.HomeSettings), Open: a.settingsMenu},
		closeItem(),
	}
}

// myAddressMenu is this Say's address (as it is given to others: the port
// only when it is not 7777): Enter on it copies it.
func (a *homeApp) myAddressMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	if sn.Self == "" {
		return []ui.HomeItem{{Key: "addr", Text: "…"}, back(), closeItem()} // the node not up yet
	}
	addr := contacts.Device{Host: sn.Self, Port: sn.Port}.Shown()
	copyIt := func() { device.CopyToClipboard(addr) }
	return []ui.HomeItem{
		{Key: "addr", Text: addr, Flash: lang.T(lang.HomeCopied), Do: copyIt, Start: true},
		back(), closeItem(),
	}
}

// dialTyped calls the address typed on the home screen.
func (a *homeApp) dialTyped(v string) {
	host, port, err := contacts.ParseAddr(v)
	if err != nil {
		ui.HomeBlink("dial")
		return
	}
	a.call(contacts.Device{Host: host, Port: port})
}

// contactsMenu: our card first, the people, add one.
func (a *homeApp) contactsMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	items := []ui.HomeItem{{Key: "me", Text: myName(sn), Online: a.personOnline(sn.Me, sn.Self), Offline: a.personOffline(sn.Me, sn.Self), Open: a.myMenu}}
	for _, p := range sortedPeople(sn, sn.People) {
		p := p
		it := ui.HomeItem{Key: "p:" + p.UID + p.Name(), Text: p.Display(sn.Display), Online: a.personOnline(p, sn.Self), Offline: a.personOffline(p, sn.Self), Open: func() []ui.HomeItem { return a.personMenu(p.UID) }}
		it.Start = p.UID != "" && p.UID == sn.Last
		items = append(items, it)
	}
	if n := len(sn.Pending); n > 0 {
		items = append(items, ui.HomeItem{Key: "pending", Text: lang.T(lang.HomePending) + " " + strconv.Itoa(n), Open: a.pendingMenu})
	}
	return append(items,
		a.addPersonItem(),
		back(), closeItem())
}

// pendingMenu: the cards received in calls from addresses not in the book,
// each by its name and the calling device's label.
func (a *homeApp) pendingMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	var items []ui.HomeItem
	for _, pd := range sn.Pending {
		pd := pd
		text := pd.Display(sn.Display)
		for _, d := range pd.Devices {
			if d.Host == pd.From && d.Label != "" {
				text += " — " + d.Label
			}
		}
		items = append(items, ui.HomeItem{Key: "pending:" + pd.From, Text: text, Open: func() []ui.HomeItem { return a.pendingCardMenu(pd) }})
	}
	return append(items, back(), closeItem())
}

// pendingCardMenu: what to do with a received card: confirm it (a new
// contact), add it to a contact of the same name, or delete it.
func (a *homeApp) pendingCardMenu(pd *contacts.Pending) []ui.HomeItem {
	sn := a.store.Snapshot()
	done := func(err error) {
		if err != nil {
			log.Printf("[contacts] a received card: %v", err)
		}
		ui.HomeBackTo("contacts")
	}
	items := []ui.HomeItem{ui.HomeItem{Key: "confirm", Text: lang.T(lang.HomeConfirm), Do: func() { done(a.store.Confirm(pd, "")) }}}
	for _, q := range sn.SameName(pd.Person) {
		q := q
		items = append(items, ui.HomeItem{Key: "into:" + q.UID, Text: lang.T(lang.HomeAddTo, q.Display(sn.Display)), Do: func() { done(a.store.Confirm(pd, q.UID)) }})
	}
	items = append(items, ui.HomeItem{Key: "delete", Text: lang.T(lang.HomeDelete), Do: func() {
		a.store.Reject(pd)
		ui.HomeBackTo("contacts")
	}})
	return append(items, back(), closeItem())
}

// sortedPeople are people by their names as shown.
func sortedPeople(sn *contacts.Snapshot, ps []*contacts.Person) []*contacts.Person {
	out := append([]*contacts.Person(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Display(sn.Display)) < strings.ToLower(out[j].Display(sn.Display))
	})
	return out
}

// myName is our card's name as shown; "…" while the book is read; "me"
// while there is no card of ours.
func myName(sn *contacts.Snapshot) string {
	switch {
	case !sn.Ready:
		return "…"
	case sn.Me != nil:
		return sn.Me.Display(sn.Display)
	}
	return lang.T(lang.HomeMe)
}

// labelText is a device's label, as its card has it; its address without.
func labelText(d contacts.Device) string {
	if d.Label == "" {
		return d.Shown()
	}
	return d.Label
}

// myMenu: our card, as anyone's (calls to our other devices);
// without one, choosing or making it.
func (a *homeApp) myMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	if sn.Me == nil {
		return []ui.HomeItem{a.myCardItem(), back(), closeItem()}
	}
	return a.personMenu(sn.Me.UID)
}

// personMenu: a call to each of the person's devices (this one shown, not
// called, nor one not there); the
// cards are edited from the settings (editContactsMenu).
func (a *homeApp) personMenu(uid string) []ui.HomeItem {
	sn := a.store.Snapshot()
	p := sn.ByUID(uid)
	if p == nil {
		return []ui.HomeItem{back(), closeItem()}
	}
	var items []ui.HomeItem
	for _, d := range p.Devices {
		d := d
		it := ui.HomeItem{Key: "call:" + d.Host, Text: lang.T(lang.HomeCallOn, labelText(d)), Online: a.deviceOnline(d.Host), Do: func() { a.call(d) },
			Offline: d.Host != sn.Self && !a.deviceOnline(d.Host), // a red ● until it answers
			Off:     d.Host == sn.Self || a.deviceOffline(d.Host)} // this device, or one not there: not called
		it.Start = d.Host == sn.LastHost[uid]
		items = append(items, it)
	}
	return append(items, back(), closeItem())
}

// deviceTitle is a device in a form's list: its label, else its address,
// else what it is to get.
func deviceTitle(d contacts.Device) string {
	if d.Label == "" && d.Host == "" {
		return lang.T(lang.HomeLabel)
	}
	return labelText(d)
}

// editPersonMenu edits a card: its names, its devices (each opens to its
// label and address), + for another. A field is written as it is left.
func (a *homeApp) editPersonMenu(uid string) []ui.HomeItem {
	sn := a.store.Snapshot()
	p := sn.ByUID(uid)
	if p == nil {
		return []ui.HomeItem{back(), closeItem()}
	}
	update := a.updater(uid)
	name := func(key, value string, ph lang.Key, need string, set func(p *contacts.Person, v string)) ui.HomeItem {
		return ui.HomeItem{Key: key, Field: &ui.HomeField{Value: value, Placeholder: lang.T(ph), Need: need, Set: func(v string) {
			update(func(p *contacts.Person) { set(p, v) })
		}}}
	}
	items := []ui.HomeItem{
		name("given", p.Given, lang.HomeGiven, "name", func(p *contacts.Person, v string) { p.Given = v }),
		name("family", p.Family, lang.HomeFamily, "name", func(p *contacts.Person, v string) { p.Family = v }),
		name("middle", p.Middle, lang.HomeMiddle, "", func(p *contacts.Person, v string) { p.Middle = v }),
		name("nick", p.Nick, lang.HomeNick, "name", func(p *contacts.Person, v string) { p.Nick = v }),
	}
	for i, d := range p.Devices {
		host, here := d.Host, d.Host == sn.Self
		items = append(items, ui.HomeItem{Key: "dev:" + strconv.Itoa(i), Text: deviceTitle(d), Open: func() []ui.HomeItem {
			return a.editDeviceMenu(uid, host, here)
		}})
	}
	// Devices being added: written once their address is in (then they
	// are the card's, at the same place in the list).
	form := "edit:" + uid
	a.mu.Lock()
	blocks := append([]*contacts.Device(nil), a.blocks[form]...)
	a.mu.Unlock()
	for n, b := range blocks {
		b := b
		a.mu.Lock()
		title := deviceTitle(*b)
		a.mu.Unlock()
		items = append(items, ui.HomeItem{Key: "dev:" + strconv.Itoa(len(p.Devices)+n), Text: title, Open: func() []ui.HomeItem {
			return a.newBlockMenu(uid, b)
		}})
	}
	items = append(items, ui.HomeItem{Key: "+", Text: "+", Bottom: true, Do: func() {
		a.mu.Lock()
		if a.blocks == nil {
			a.blocks = map[string][]*contacts.Device{}
		}
		a.blocks[form] = append(a.blocks[form], &contacts.Device{Port: contacts.DefaultPort})
		key := "dev:" + strconv.Itoa(len(p.Devices)+len(a.blocks[form])-1)
		a.mu.Unlock()
		ui.HomeOpenKey(key) // the new device, entered at once
	}})
	if sn.Me == nil || sn.Me.UID != uid { // − : the person out of Say (not our own card)
		items = append(items, deleteItem(func() {
			if err := a.store.Remove(uid); err != nil {
				log.Printf("[contacts] %v", err)
			}
		}))
	}
	return append(items, back(), closeItem())
}

// updater writes changes to a card.
func (a *homeApp) updater(uid string) func(f func(p *contacts.Person)) {
	return func(f func(p *contacts.Person)) {
		if _, err := a.store.Update(uid, f); err != nil {
			log.Printf("[contacts] %v", err)
		}
	}
}

// editDeviceMenu: a card's device, its label and address (this device's
// address is its own: shown, not edited). − under the line removes the
// device (pressed twice), as erasing its address does.
func (a *homeApp) editDeviceMenu(uid, host string, here bool) []ui.HomeItem {
	p := a.store.Snapshot().ByUID(uid)
	var d contacts.Device
	if p != nil {
		for _, x := range p.Devices {
			if x.Host == host {
				d = x
			}
		}
	}
	update := a.updater(uid)
	device := func(f func(d *contacts.Device)) {
		update(func(p *contacts.Person) {
			for i := range p.Devices {
				if p.Devices[i].Host == host {
					f(&p.Devices[i])
					host = p.Devices[i].Host
					return
				}
			}
		})
	}
	items := []ui.HomeItem{{Key: "label", Field: &ui.HomeField{Value: d.Label, Placeholder: lang.T(lang.HomeLabel), Set: func(v string) {
		device(func(d *contacts.Device) { d.Label = v })
	}}}}
	if here {
		// This device's address: shown, not edited (it is this Say's own).
		items = append(items, ui.HomeItem{Key: "addr", Text: d.Shown()})
	} else {
		remove := func() { // the device goes, back to the card
			update(func(p *contacts.Person) {
				kept := p.Devices[:0]
				for _, x := range p.Devices {
					if x.Host != host {
						kept = append(kept, x)
					}
				}
				p.Devices = kept
			})
			ui.HomeBackTo("given")
		}
		items = append(items, ui.HomeItem{Key: "addr", Field: &ui.HomeField{Value: d.Shown(), Placeholder: lang.T(lang.HomeAddress), Set: func(v string) {
			if v == "" { // erased: the device goes
				remove()
				return
			}
			if h, port, err := contacts.ParseAddr(v); err == nil { // [address]:port, the port where not 7777
				device(func(d *contacts.Device) { d.Host, d.Port = h, port })
			}
		}}})
		items = append(items, deleteItem(remove))
	}
	return append(items, back(), closeItem())
}

// deleteItem is − under the line, red: its first press makes it read
// "Delete forever" (moving off takes that back), the second does del.
func deleteItem(del func()) ui.HomeItem {
	return ui.HomeItem{Key: "del", Text: "−", Bottom: true, Danger: true, Confirm: lang.T(lang.HomeDeleteForever), Do: del}
}

// newBlockMenu: a device being added to a card: its label, then its
// address, with which it is written.
func (a *homeApp) newBlockMenu(uid string, b *contacts.Device) []ui.HomeItem {
	form := "edit:" + uid
	a.mu.Lock()
	label := b.Label
	a.mu.Unlock()
	return []ui.HomeItem{
		{Key: "label", Field: &ui.HomeField{Value: label, Placeholder: lang.T(lang.HomeLabel), Set: func(v string) {
			a.mu.Lock()
			b.Label = v
			a.mu.Unlock()
		}}},
		{Key: "addr", Field: &ui.HomeField{Placeholder: lang.T(lang.HomeAddress), Need: "addr", Set: func(v string) {
			h, port, err := contacts.ParseAddr(v)
			if err != nil {
				return
			}
			a.mu.Lock()
			d := contacts.Device{Label: b.Label, Host: h, Port: port}
			kept := a.blocks[form][:0]
			for _, x := range a.blocks[form] {
				if x != b {
					kept = append(kept, x)
				}
			}
			a.blocks[form] = kept
			a.mu.Unlock()
			a.updater(uid)(func(p *contacts.Person) {
				if !p.Has(d.Host) {
					p.Devices = append(p.Devices, d)
				}
			})
		}}},
		back(), closeItem(),
	}
}

// draft is a card being made, kept while its form is open.
type draft struct {
	uid    string
	given  string
	family string
	middle string
	nick   string
	devs   []*contacts.Device // a new person's devices
}

func (dr *draft) names(p *contacts.Person) {
	p.Given, p.Family, p.Middle, p.Nick = dr.given, dr.family, dr.middle, dr.nick
}

func (dr *draft) named() bool { return dr.given+dr.family+dr.nick != "" }

// draftField is a line of a draft's form: its value as it is now, set into
// the draft (under the lock), then commit (nil: nothing more); need names
// the group of fields of which one must be filled ("": none).
func (a *homeApp) draftField(need, key string, v *string, ph lang.Key, commit func()) ui.HomeItem {
	a.mu.Lock()
	value := *v
	a.mu.Unlock()
	return ui.HomeItem{Key: key, Field: &ui.HomeField{Value: value, Placeholder: lang.T(ph), Need: need, Set: func(s string) {
		a.mu.Lock()
		*v = s
		a.mu.Unlock()
		if commit != nil {
			commit()
		}
	}}}
}

// newPersonMenu adds a person: names, their devices (each opens to its
// label and address), + for another. Nothing is written until the form is
// left (savePerson): then, with a name and an address, the card is made.
func (a *homeApp) newPersonMenu() []ui.HomeItem {
	dr := a.draftFor("person")
	a.mu.Lock()
	devs := append([]*contacts.Device(nil), dr.devs...)
	titles, any := make([]string, len(devs)), false
	for i, d := range devs {
		titles[i] = deviceTitle(*d)
		any = any || d.Host != ""
	}
	a.mu.Unlock()
	items := []ui.HomeItem{
		a.draftField("name", "given", &dr.given, lang.HomeGiven, nil),
		a.draftField("name", "family", &dr.family, lang.HomeFamily, nil),
		a.draftField("", "middle", &dr.middle, lang.HomeMiddle, nil),
		a.draftField("name", "nick", &dr.nick, lang.HomeNick, nil),
	}
	for i, d := range devs {
		d := d
		items = append(items, ui.HomeItem{Key: "dev:" + strconv.Itoa(i), Text: titles[i], Open: func() []ui.HomeItem {
			return a.draftDeviceMenu(d)
		}})
	}
	// + : another device, entered at once; it blinks while the person has
	// no address.
	items = append(items, ui.HomeItem{Key: "+", Text: "+", Bottom: true, Need: "addr", Filled: any, Do: func() {
		a.mu.Lock()
		dr.devs = append(dr.devs, &contacts.Device{Port: contacts.DefaultPort})
		key := "dev:" + strconv.Itoa(len(dr.devs)-1)
		a.mu.Unlock()
		ui.HomeOpenKey(key)
	}})
	return append(items, back(), closeItem())
}

// draftDeviceMenu: a new person's device, its label and address.
func (a *homeApp) draftDeviceMenu(d *contacts.Device) []ui.HomeItem {
	a.mu.Lock()
	label, addr := d.Label, ""
	if d.Host != "" {
		addr = d.Shown()
	}
	a.mu.Unlock()
	return []ui.HomeItem{
		{Key: "label", Field: &ui.HomeField{Value: label, Placeholder: lang.T(lang.HomeLabel), Set: func(v string) {
			a.mu.Lock()
			d.Label = v
			a.mu.Unlock()
		}}},
		{Key: "addr", Field: &ui.HomeField{Value: addr, Placeholder: lang.T(lang.HomeAddress), Need: "addr", Set: func(v string) {
			h, port, err := contacts.ParseAddr(v)
			if v != "" && err != nil {
				return
			}
			a.mu.Lock()
			d.Host, d.Port = h, port
			a.mu.Unlock()
		}}},
		back(), closeItem(),
	}
}

// savePerson is leaving the new person's form: with a name and an address
// the card is made, else nothing is; the form starts afresh next time.
// (The menu's lock is held: no menu calls.)
func (a *homeApp) savePerson() {
	a.mu.Lock()
	dr := a.drafts["person"]
	delete(a.drafts, "person")
	var devs []contacts.Device
	if dr != nil {
		for _, d := range dr.devs {
			if d.Host != "" {
				devs = append(devs, *d)
			}
		}
	}
	a.mu.Unlock()
	if dr == nil || !dr.named() || len(devs) == 0 {
		return
	}
	if _, err := a.store.Update("", func(p *contacts.Person) {
		dr.names(p)
		p.Devices = devs
	}); err != nil {
		log.Printf("[contacts] new person: %v", err)
	}
}

// ---- settings ----

// settingsMenu: where the contacts are, the system's address book (where
// there is one) or a folder; the one in use is ticked.
func (a *homeApp) settingsMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	sys := sn.Source == "os"
	var items []ui.HomeItem
	if contacts.OSBookAvailable() {
		// Entering it chooses it (ticked; the system asks for access the
		// first time) and opens our card in it.
		items = append(items, ui.HomeItem{Key: "os", Text: lang.T(lang.HomeOSBook), Tick: sys, Start: sys, Open: a.osMenu, Enter: func() {
			if !sys {
				a.useOS()
			}
		}})
	}
	// Entering the folder chooses it too (ticked), once there is one.
	items = append(items, ui.HomeItem{Key: "folder", Text: lang.T(lang.HomeFolder), Tick: !sys && sn.Dir != "", Start: !sys, Open: a.folderMenu, Enter: func() {
		if sys && sn.Dir != "" {
			a.useFolder(sn.Dir)
		}
	}})
	if it, ok := a.iconItem(); ok {
		items = append(items, it)
	}
	return append(items, back(), closeItem())
}

// osMenu: our card in the system's address book, its label, how names show.
func (a *homeApp) osMenu() []ui.HomeItem {
	return append(append(a.myCardItems(), a.displayItem(), a.editContactsItem()), back(), closeItem())
}

// useOS switches to the system's address book once the system lets Say
// read it (the first time it asks the user: aside, the menu goes on).
func (a *homeApp) useOS() {
	go func() {
		if err := a.store.UseSystem(); err != nil {
			logs.LogV("[contacts] the system's address book: %v", err)
			ui.HomeBackTo("os")
			ui.HomeFlash("os", lang.T(lang.HomeNoAccess))
		}
	}()
}

// useFolder makes dir where the contacts come from.
func (a *homeApp) useFolder(dir string) {
	if err := a.store.UseFolder(dir); err != nil {
		log.Printf("[contacts] %s: %v", dir, err)
	}
}

// displayItem: how people are named in the list.
func (a *homeApp) displayItem() ui.HomeItem {
	return ui.HomeItem{Key: "display", Text: lang.T(lang.HomeDisplay), Open: a.displayMenu}
}

// editContactsItem: under how people are named, the cards to edit.
func (a *homeApp) editContactsItem() ui.HomeItem {
	return ui.HomeItem{Key: "editall", Text: lang.T(lang.HomeEditContacts), Open: a.editContactsMenu}
}

// addPersonItem is + under the line: a new person's form (the contacts,
// the cards to edit).
func (a *homeApp) addPersonItem() ui.HomeItem {
	return ui.HomeItem{Key: "+", Text: "+", Bottom: true, Open: a.newPersonMenu, Enter: a.newDraft("person"), Leave: a.savePerson}
}

// editContactsMenu: our card first, then the people, each opening its
// form; + adds one, as in the contacts.
func (a *homeApp) editContactsMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	var items []ui.HomeItem
	edit := func(key, text, uid string) ui.HomeItem {
		form := "edit:" + uid
		return ui.HomeItem{Key: key, Text: text, Open: func() []ui.HomeItem { return a.editPersonMenu(uid) },
			Enter: a.newDraft(form), Leave: a.newDraft(form)}
	}
	if sn.Me != nil {
		items = append(items, edit("me", myName(sn), sn.Me.UID))
	}
	for _, p := range sortedPeople(sn, sn.People) {
		items = append(items, edit("p:"+p.UID+p.Name(), p.Display(sn.Display), p.UID))
	}
	return append(items, a.addPersonItem(), back(), closeItem())
}

// displayMenu is the ways to name people, the one chosen ticked; each shown
// as our own name looks in it (its name where our card lacks what it takes).
func (a *homeApp) displayMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	styles := []struct {
		style string
		name  lang.Key
	}{
		{contacts.ShowNick, lang.HomeNick},
		{contacts.ShowGivenFamily, lang.HomeShowGivenFamily},
		{contacts.ShowFamilyGiven, lang.HomeShowFamilyGiven},
		{contacts.ShowShort, lang.HomeShowShort},
	}
	var items []ui.HomeItem
	seen := map[string]bool{}
	for _, st := range styles {
		st := st
		text := lang.T(st.name)
		if me := sn.Me; me != nil {
			if ex := me.Display(st.style); ex != "" && !seen[ex] && (st.style != contacts.ShowNick || me.Nick != "") &&
				(st.style != contacts.ShowShort || me.Family != "") {
				text = ex
			}
		}
		seen[text] = true
		items = append(items, ui.HomeItem{Key: "show:" + st.style, Text: text, Tick: st.style == sn.Display, Start: st.style == sn.Display, Do: func() {
			a.store.SetDisplay(st.style)
		}})
	}
	return append(items, back(), closeItem())
}

// myCardItems: our card's line (the cards, ours ticked), and this device's
// label once there is a card of ours.
func (a *homeApp) myCardItems() []ui.HomeItem {
	items := []ui.HomeItem{a.myCardItem()}
	if a.store.Snapshot().Me != nil {
		items = append(items, ui.HomeItem{Key: "label", Text: lang.T(lang.HomeThisLabel), Open: a.thisLabelMenu})
	}
	return items
}

// myCardItem is our card's line: the cards, ours ticked.
func (a *homeApp) myCardItem() ui.HomeItem {
	return ui.HomeItem{Key: "card", Text: lang.T(lang.HomeMyCard), Open: a.pickMeMenu}
}

// pickMeMenu is every card, ours ticked; choosing one makes it ours (in a
// folder its file is marked "me"; this Say's address goes on it). In a
// folder, + makes a new one.
func (a *homeApp) pickMeMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	var items []ui.HomeItem
	for _, p := range sortedPeople(sn, sn.All) {
		p := p
		mine := sn.Me != nil && p.UID == sn.Me.UID
		items = append(items, ui.HomeItem{Key: "p:" + p.UID, Text: p.Display(sn.Display), Tick: mine, Start: mine, Do: func() {
			if err := a.store.TakeAsMe(p.UID); err != nil {
				log.Printf("[contacts] our card: %v", err)
			}
		}})
	}
	if sn.Source != "os" {
		items = append(items, ui.HomeItem{Key: "+", Text: "+", Bottom: true, Open: a.createMeMenu, Enter: a.newDraft("me"), Leave: a.newDraft("me")})
	}
	return append(items, back(), closeItem())
}

// createMeMenu makes our card: its names (this Say's address goes on it,
// labelled with this device's kind), written as soon as it has one.
func (a *homeApp) createMeMenu() []ui.HomeItem {
	dr := a.draftFor("me")
	commit := func() {
		a.mu.Lock()
		c := *dr
		a.mu.Unlock()
		if !c.named() {
			return
		}
		// The card made stays this form's even if marking it ours failed:
		// the next field changes it, not another card.
		uid, err := a.store.CreateMe(c.uid, c.names)
		if err != nil {
			log.Printf("[contacts] our card: %v", err)
		}
		if uid != "" {
			a.mu.Lock()
			dr.uid = uid
			a.mu.Unlock()
		}
	}
	return []ui.HomeItem{
		a.draftField("name", "given", &dr.given, lang.HomeGiven, commit),
		a.draftField("name", "family", &dr.family, lang.HomeFamily, commit),
		a.draftField("", "middle", &dr.middle, lang.HomeMiddle, commit),
		a.draftField("name", "nick", &dr.nick, lang.HomeNick, commit),
		back(), closeItem(),
	}
}

// thisLabelMenu is this device's label on our card, typed in at once;
// erased, it is the device's kind again (laptop, computer).
func (a *homeApp) thisLabelMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	if sn.Me == nil {
		return []ui.HomeItem{back(), closeItem()}
	}
	current := ""
	for _, d := range sn.Me.Devices {
		if d.Host == sn.Self {
			current = d.Label
			break
		}
	}
	return []ui.HomeItem{
		ui.HomeItem{Key: "label", Start: true, Field: &ui.HomeField{Value: current, Placeholder: sn.Kind, Set: func(v string) {
			if v == "" {
				v = sn.Kind
			}
			if err := a.store.SetHereLabel(v); err != nil {
				log.Printf("[contacts] this device's label: %v", err)
			}
		}}},
		back(), closeItem(),
	}
}

// ---- the folder ----

// folderMenu: choose a folder (from the top), or the folder's path (to
// change it, from where it is) and our card in it.
func (a *homeApp) folderMenu() []ui.HomeItem {
	sn := a.store.Snapshot()
	if sn.Dir == "" {
		return []ui.HomeItem{{Key: "dir", Text: lang.T(lang.HomeChooseFolder), Open: func() []ui.HomeItem { return a.browse(fsRoot()) }, Enter: a.newBrowse}, back(), closeItem()}
	}
	// The folder's path: entering it shows the folders from the top down to
	// the ones ours is among, ours ticked in the middle; < goes up a level.
	dir := sn.Dir
	items := []ui.HomeItem{{Key: "dir", Text: tilde(dir), Open: func() []ui.HomeItem { return a.browse(fsRoot()) }, Enter: func() {
		a.newBrowse()
		ui.HomeDescend(pathKeys(filepath.Dir(dir)))
	}}}
	if sn.Source != "os" {
		items = append(items, a.myCardItems()...)
		items = append(items, a.displayItem(), a.editContactsItem())
	}
	return append(items, back(), closeItem())
}

// tilde shows a path in the home folder as ~/...
func tilde(p string) string {
	h := homeDir()
	if p == h {
		return "~"
	}
	if strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

// newBrowse starts a folder chooser: each folder is read once while it is
// open.
func (a *homeApp) newBrowse() {
	a.mu.Lock()
	a.browsed = map[string][]string{}
	a.mu.Unlock()
}

// folders is a folder's folders (links to folders too), by name.
func (a *homeApp) folders(dir string) []string {
	a.mu.Lock()
	if list, ok := a.browsed[dir]; ok {
		a.mu.Unlock()
		return list
	}
	a.mu.Unlock()
	entries, _ := os.ReadDir(dir)
	var list []string
	for _, e := range entries {
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			// A link to a folder is a folder (/var, /tmp on macOS).
			if fi, err := os.Stat(filepath.Join(dir, e.Name())); err != nil || !fi.IsDir() {
				continue
			}
		}
		list = append(list, e.Name())
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i]) < strings.ToLower(list[j]) })
	a.mu.Lock()
	if a.browsed == nil {
		a.browsed = map[string][]string{}
	}
	a.browsed[dir] = list
	a.mu.Unlock()
	return list
}

// pathKeys are the browse items from the top down to dir: its folders' keys.
func pathKeys(dir string) []string {
	rel, err := filepath.Rel(fsRoot(), dir)
	if err != nil || rel == "." {
		return nil
	}
	var keys []string
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		keys = append(keys, "dir:"+part)
	}
	return keys
}

func fsRoot() string {
	if runtime.GOOS == "windows" {
		return filepath.VolumeName(homeDir()) + `\`
	}
	return "/"
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

// browse is a folder's folders (the one in use ticked): going into one
// chooses it (one without folders is only chosen); < goes back up.
func (a *homeApp) browse(dir string) []ui.HomeItem {
	current := a.store.Snapshot().Dir
	var items []ui.HomeItem
	for _, name := range a.folders(dir) {
		sub := filepath.Join(dir, name)
		// Hidden folders are left out, but for the one on the way to ours.
		if strings.HasPrefix(name, ".") && sub != current && !strings.HasPrefix(current, sub+string(filepath.Separator)) {
			continue
		}
		it := ui.HomeItem{Key: "dir:" + name, Text: name, Leaf: true, Open: func() []ui.HomeItem { return a.browse(sub) }, Enter: func() {
			a.useFolder(sub)
		}}
		switch {
		case sub == current: // the folder in use: ticked, the list at it
			it.Tick, it.Start = true, true
		case strings.HasPrefix(current, sub+string(filepath.Separator)): // on the way to it
			it.Start = true
		}
		items = append(items, it)
	}
	// x: back to the folder's menu, where the way down began.
	return append(items, back(), ui.HomeItem{Key: "x", Close: true, CloseTo: "dir"})
}
