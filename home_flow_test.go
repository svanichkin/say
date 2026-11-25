package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/svanichkin/say/contacts"
	"github.com/svanichkin/say/keys"
	"github.com/svanichkin/say/lang"
	"github.com/svanichkin/say/ui"
	"golang.org/x/sys/unix"
)

func pressKeys(t *testing.T, rs ...rune) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for _, r := range rs {
			ui.KeyInput(r)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		buf := make([]byte, 1<<20)
		t.Fatalf("hung after %q:\n%s", rs, buf[:runtime.Stack(buf, true)])
	}
}

func writeCard(t *testing.T, path, card string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
}

// eventually waits for cond (the book read again aside).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Changing the folder: its path shows the folders it is among, it ticked;
// entering another (without folders: only chosen) chooses it.
func TestHomeChangeFolder(t *testing.T) {
	home := t.TempDir()
	old, other := filepath.Join(home, "Contacts"), filepath.Join(home, "Other")
	os.Mkdir(old, 0o755)
	os.Mkdir(other, 0o755)
	cfg := filepath.Join(t.TempDir(), "server.json")
	a := newHomeApp(cfg, old, "200::1", 7777)
	ui.SetHome(a.root, a.paste, func() {})
	ui.ShowHome(true)
	defer func() { ui.SetHome(nil, nil, nil); ui.ShowHome(false) }()

	pressKeys(t, keys.Down, keys.Down, keys.Down, '\r') // settings (after contacts, an address to call, ours)
	pressKeys(t, '\r')                                  // the folder: in use
	pressKeys(t, '\r')                                  // its path: the folders it is among, at it
	col := a.browse(home)
	if col[0].Text != "Contacts" || !col[0].Tick || !col[0].Start {
		t.Fatalf("the folder in use among the others: %+v", col[0])
	}
	pressKeys(t, keys.Down, '\r') // Other: entering it chooses it
	if sn := a.store.Snapshot(); sn.Dir != other || sn.Source != "" {
		t.Fatalf("the folder is %q (%q), want %q", sn.Dir, sn.Source, other)
	}
	if col = a.browse(home); col[1].Text != "Other" || !col[1].Tick || col[0].Tick {
		t.Fatalf("the tick did not move: %q, %q", col[0].Text, col[1].Text)
	}
	saved, _ := contacts.LoadSettings(contacts.SettingsPath(cfg))
	if saved.Dir != other {
		t.Fatalf("the settings file has %q", saved.Dir)
	}
}

// Our card is the card with this Say's address: made with + (names only;
// the address goes on it labelled with the device's kind), first in the
// contacts, its label chosen in the settings.
func TestMyCard(t *testing.T) {
	dir := t.TempDir()
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::1", 7000)
	if cards := a.pickMeMenu(); cards[0].Key != "+" {
		t.Fatalf("an empty folder: %+v", cards[0])
	}
	form := a.createMeMenu()
	if len(form) != 6 { // names and the nickname, then < and x
		t.Fatalf("the new card's form has %d lines", len(form))
	}
	form[0].Field.Set("Sergey")
	sn := a.store.Snapshot()
	if sn.Me == nil || len(sn.Me.Devices) != 1 || sn.Me.Devices[0] != (contacts.Device{Label: sn.Kind, Host: "200::1", Port: 7000}) {
		t.Fatalf("our card: %+v", sn.Me)
	}
	if got := a.contactsMenu()[0].Text; got != "Sergey" {
		t.Fatalf("the contacts' first line is %q", got)
	}
	if cards := a.pickMeMenu(); cards[0].Text != "Sergey" || !cards[0].Tick {
		t.Fatalf("our card in the list: %q", cards[0].Text)
	}
	folder := a.folderMenu()
	if folder[0].Text != tilde(dir) || folder[1].Key != "card" || folder[2].Key != "label" || folder[3].Key != "display" {
		t.Fatalf("the folder's menu: %q %q %q %q", folder[0].Text, folder[1].Key, folder[2].Key, folder[3].Key)
	}

	// This device's label: one field, typed in; erased, the kind again.
	label := a.thisLabelMenu()
	if label[0].Field == nil || label[0].Field.Value != sn.Kind || label[0].Field.Placeholder != sn.Kind {
		t.Fatalf("the label column: %+v", label[0])
	}
	label[0].Field.Set("ноутбук-2")
	if me := a.store.Snapshot().Me; me.Devices[0].Label != "ноутбук-2" {
		t.Fatalf("after naming it: %+v", me.Devices)
	}
	a.thisLabelMenu()[0].Field.Set("")
	if me := a.store.Snapshot().Me; me.Devices[0].Label != sn.Kind {
		t.Fatalf("after erasing it: %+v", me.Devices)
	}
}

// Our card is the one whose file is marked "me", whatever its name (a
// rename keeps the mark); this Say's address is put on it; choosing
// another moves the mark and the address there.
func TestMeByMark(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no extended attributes")
	}
	dir := t.TempDir()
	geek := filepath.Join(dir, "Я.vcf")
	writeCard(t, geek, "BEGIN:VCARD\nVERSION:3.0\nUID:ME-4\nFN:mr.Geek\nNICKNAME:mr.Geek\nEND:VCARD\n")
	writeCard(t, filepath.Join(dir, "other.vcf"), "BEGIN:VCARD\nVERSION:3.0\nUID:ME-5\nFN:Сергей\nEND:VCARD\n")
	name := "me"
	if runtime.GOOS == "linux" {
		name = "user.me"
	}
	if err := unix.Setxattr(geek, name, []byte("ME-4"), 0); err != nil {
		t.Skip("the file system takes no attributes:", err)
	}
	os.Rename(geek, filepath.Join(dir, "mr.Geek.vcf")) // another program renames it
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::9", 7000)
	sn := a.store.Snapshot()
	if sn.Me == nil || sn.Me.UID != "ME-4" || !sn.Me.Has("200::9") || sn.Me.Devices[0].Label != sn.Kind {
		t.Fatalf("our card: %+v", sn.Me)
	}
	if menu := a.contactsMenu(); menu[0].Text != "mr.Geek" || len(menu) != 4 { // me, +, <, x: not twice
		t.Fatalf("the contacts: %q and %d more", menu[0].Text, len(menu)-1)
	}
	if err := a.store.TakeAsMe("ME-5"); err != nil {
		t.Fatal(err)
	}
	sn = a.store.Snapshot()
	if sn.Me == nil || sn.Me.UID != "ME-5" || !sn.Me.Has("200::9") {
		t.Fatalf("our card after choosing: %+v", sn.Me)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "mr.Geek.vcf"))
	if strings.Contains(string(data), "200::9") {
		t.Fatalf("the address stayed on the old card:\n%s", data)
	}
	if _, err := unix.Getxattr(filepath.Join(dir, "mr.Geek.vcf"), name, make([]byte, 64)); err == nil {
		t.Fatal("the old card's file is still marked")
	}
}

// A card changed on disk by another program is read again: the system
// tells of it.
func TestFolderChangedOnDisk(t *testing.T) {
	dir := t.TempDir()
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::5", 7777)
	writeCard(t, filepath.Join(dir, "bob.vcf"), "BEGIN:VCARD\nVERSION:3.0\nFN:Bob\nitem1.URL:say:[201::1]\nEND:VCARD\n")
	eventually(t, "Bob read", func() bool { return len(a.store.Snapshot().People) == 1 })
}

// The list names people as chosen: the choice ticked, each shown as our
// own name looks in it.
func TestDisplay(t *testing.T) {
	dir := t.TempDir()
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::1", 7777)
	form := a.createMeMenu()
	form[0].Field.Set("Сергей")
	form[1].Field.Set("Ваничкин")
	form[2].Field.Set("Сергеевич")
	form[3].Field.Set("alien")
	show := a.displayMenu()
	got := []string{show[0].Text, show[1].Text, show[2].Text, show[3].Text}
	want := []string{"alien", "Сергей Ваничкин", "Ваничкин Сергей", "С.С.Ваничкин"}
	for i := range want {
		if got[i] != want[i] || show[i].Tick != (i == 1) {
			t.Fatalf("the choices %q (ticked: the second), want %q", got, want)
		}
	}
	show[3].Do()
	if a.contactsMenu()[0].Text != "С.С.Ваничкин" || a.store.Snapshot().Display != contacts.ShowShort {
		t.Fatalf("after choosing: %q", a.contactsMenu()[0].Text)
	}
}

// A folder with no file marked "me" (another program rewrote ours): the one
// card with this Say's address is marked again, ours, not listed twice.
func TestMarkLostOneCardWithOurAddress(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no extended attributes")
	}
	dir := t.TempDir()
	writeCard(t, filepath.Join(dir, "mr.Geek.vcf"), "BEGIN:VCARD\nVERSION:3.0\nUID:ME-6\nFN:mr.Geek\nitem1.URL:say:[200::6]:7000\nitem1.X-ABLabel:ноутбук\nEND:VCARD\n")
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "", 7000)
	a.setSelf("200::6") // the node's address comes later
	sn := a.store.Snapshot()
	if sn.Me == nil || sn.Me.UID != "ME-6" || len(sn.People) != 0 {
		t.Fatalf("our card %+v, people %d", sn.Me, len(sn.People))
	}
	name := "me"
	if runtime.GOOS == "linux" {
		name = "user.me"
	}
	if _, err := unix.Getxattr(filepath.Join(dir, "mr.Geek.vcf"), name, make([]byte, 64)); err != nil {
		t.Fatal("not marked again:", err)
	}
}

func keysOf(items []ui.HomeItem) string {
	var ks []string
	for _, it := range items {
		ks = append(ks, it.Key)
	}
	return strings.Join(ks, " ")
}

// A new person: names, + opens a device (label, address); nothing written
// while the form is open, the card made as it is left (with a name and an
// address); the form starts afresh.
func TestNewPerson(t *testing.T) {
	dir := t.TempDir()
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::1", 7777)
	ui.SetHome(a.root, a.paste, func() {})
	ui.ShowHome(true)
	defer func() { ui.SetHome(nil, nil, nil); ui.ShowHome(false) }()
	pressKeys(t, '\r') // contacts
	for range a.contactsMenu()[:len(a.contactsMenu())-3] {
		pressKeys(t, keys.Down)
	}
	pressKeys(t, '\r')                          // +: the form
	pressKeys(t, 'A', 'n', 'n', 'a', keys.Down) // the name, kept
	if got := keysOf(a.newPersonMenu()); got != "given family middle nick + < x" {
		t.Fatalf("the form: %s", got)
	}
	pressKeys(t, keys.Down, keys.Down, keys.Down, '\r') // +: a device, entered at once
	pressKeys(t, 'h', 'o', 'm', 'e', keys.Down)
	for _, r := range "[201::5]:7001" {
		pressKeys(t, r)
	}
	pressKeys(t, keys.Down) // the address set
	if len(a.store.Snapshot().People) != 0 {
		t.Fatal("written while the form is open")
	}
	if got := a.newPersonMenu(); got[4].Text != "home" {
		t.Fatalf("the device in the form: %q", got[4].Text)
	}
	pressKeys(t, keys.Left, keys.Left) // out of the device, out of the form: made
	eventually(t, "Anna written", func() bool { return len(a.store.Snapshot().People) == 1 })
	p := a.store.Snapshot().People[0]
	if p.Given != "Anna" || len(p.Devices) != 1 || p.Devices[0] != (contacts.Device{Label: "home", Host: "201::5", Port: 7001}) {
		t.Fatalf("Anna: %+v", p)
	}
	if got := keysOf(a.newPersonMenu()); got != "given family middle nick + < x" || a.newPersonMenu()[0].Field.Value != "" {
		t.Fatalf("the form after: %s", got)
	}
}

// A person's menu: a call to each device, then editing: names, the devices
// (each opening to its label and address; an address erased removes it),
// + for another, written once its address is in.
func TestEditPerson(t *testing.T) {
	dir := t.TempDir()
	writeCard(t, filepath.Join(dir, "bob.vcf"), "BEGIN:VCARD\nVERSION:3.0\nUID:BOB\nN:Smith;Bob;;;\nFN:Bob Smith\n"+
		"item1.URL:say:[201::1]\nitem1.X-ABLabel:home\nitem2.URL:say:[201::2]:7001\nitem2.X-ABLabel:phone\nEND:VCARD\n")
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::1", 7777)
	menu := a.personMenu("BOB")
	if len(menu) != 5 || menu[0].Text != lang.T(lang.HomeCallOn, "home") || menu[1].Text != lang.T(lang.HomeCallOn, "phone") || menu[2].Key != "edit" {
		t.Fatalf("Bob's menu: %q %q %q", menu[0].Text, menu[1].Text, menu[2].Key)
	}
	form := a.editPersonMenu("BOB")
	if got := keysOf(form); got != "given family middle nick dev:0 dev:1 + < x" || form[5].Text != "phone" {
		t.Fatalf("the form: %s (%q)", got, form[5].Text)
	}
	if phone := form[5].Open(); phone[1].Field.Value != "[201::2]:7001" {
		t.Fatalf("the phone's address shown as %q", phone[1].Field.Value)
	}
	form[0].Field.Set("Robert")
	form[4].Open()[1].Field.Set("") // the home's address erased: it goes
	p := a.store.Snapshot().ByUID("BOB")
	if p.Given != "Robert" || len(p.Devices) != 1 || p.Devices[0].Label != "phone" {
		t.Fatalf("after editing: %q %+v", p.Given, p.Devices)
	}
	form = a.editPersonMenu("BOB")
	form[len(form)-3].Do() // +
	form = a.editPersonMenu("BOB")
	if got := keysOf(form); got != "given family middle nick dev:0 dev:1 + < x" {
		t.Fatalf("the form after +: %s", got)
	}
	block := form[5].Open()
	block[0].Field.Set("work")
	block[1].Field.Set("[202::3]:7002")
	p = a.store.Snapshot().ByUID("BOB")
	if len(p.Devices) != 2 || p.Devices[1] != (contacts.Device{Label: "work", Host: "202::3", Port: 7002}) {
		t.Fatalf("the new device: %+v", p.Devices)
	}
	if got := keysOf(a.editPersonMenu("BOB")); got != "given family middle nick dev:0 dev:1 + < x" {
		t.Fatalf("after writing it: %s", got)
	}
}

// This device on our card opens to its label and its address, shown, not
// a field.
func TestThisDeviceShowsAddress(t *testing.T) {
	dir := t.TempDir()
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), dir, "200::1", 7000)
	a.createMeMenu()[0].Field.Set("mr.Geek")
	me := a.store.Snapshot().Me
	if me == nil {
		t.Skip("no extended attributes: no card of ours in a folder")
	}
	col := a.editDeviceMenu(me.UID, "200::1", true)
	if col[0].Field == nil || col[1].Key != "addr" || col[1].Field != nil || col[1].Text != "[200::1]:7000" {
		t.Fatalf("this device's column: %+v / %+v", col[0], col[1])
	}
}

// An address typed on the home screen and Enter is called; not an address,
// nothing is.
func TestDialTyped(t *testing.T) {
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), t.TempDir(), "200::1", 7777)
	called := make(chan contacts.Device, 1)
	a.setDial(func(ctx context.Context, d contacts.Device) error { called <- d; return nil })
	ui.SetHome(a.root, a.paste, func() {})
	ui.ShowHome(true)
	defer func() { ui.SetHome(nil, nil, nil); ui.ShowHome(false) }()
	pressKeys(t, keys.Down) // the address field: typed in at once
	for _, r := range "nonsense" {
		pressKeys(t, r)
	}
	pressKeys(t, '\r')
	select {
	case d := <-called:
		t.Fatalf("called %+v", d)
	case <-time.After(200 * time.Millisecond):
	}
	for _, r := range "[201::7]:7002" {
		pressKeys(t, r)
	}
	pressKeys(t, '\r')
	select {
	case d := <-called:
		if d.Host != "201::7" || d.Port != 7002 {
			t.Fatalf("called %+v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("not called")
	}
}

// My address: this Say's, the port only when it is not 7777.
func TestMyAddress(t *testing.T) {
	a := newHomeApp(filepath.Join(t.TempDir(), "c.json"), t.TempDir(), "200::1", 7001)
	col := a.myAddressMenu()
	if col[0].Text != "[200::1]:7001" || col[0].Do == nil || col[1].Text != lang.T(lang.HomeCopy) || col[1].Flash != lang.T(lang.HomeCopied) {
		t.Fatalf("my address: %+v / %+v", col[0], col[1])
	}
}
