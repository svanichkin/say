package contacts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A card as macOS Contacts exports it: folded photo, labels in groups, one
// Say device on the default port and one on another.
const aliceCard = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"PRODID:-//Apple Inc.//macOS 15.0//EN\r\n" +
	"N:Ivanova;Alice;Petrovna;;\r\n" +
	"FN:Alice Ivanova\r\n" +
	"TEL;type=CELL;type=VOICE;type=pref:+7 900 000-00-00\r\n" +
	"PHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAMCAgICAgMCAgIDAwMDBAYEBAQEBAgGBgUGCQgKCgkICQ\r\n" +
	" kKDA8MCgsOCwkJDRENDg8QEBEQCgwSExIQEw8QEBD/\r\n" +
	"item1.IMPP;X-SERVICE-TYPE=Say:say://[200:abcd::1]\r\n" +
	"item1.X-ABLabel:_$!<Home>!$_\r\n" +
	"item2.IMPP;X-SERVICE-TYPE=Say:say://[201:1::2]:7001\r\n" +
	"item2.X-ABLabel:phone\r\n" +
	"item3.URL:https://example.org\r\n" +
	"item3.X-ABLabel:_$!<HomePage>!$_\r\n" +
	"UID:AAAA-1\r\n" +
	"END:VCARD\r\n"

// Two cards in one file: one without Say (left out), one with a TYPE label.
const exportCards = "BEGIN:VCARD\nVERSION:2.1\nN:Smith;Bob\nFN:Bob Smith\nTEL;HOME:123\nEND:VCARD\n" +
	"BEGIN:VCARD\nVERSION:4.0\nFN:Carol\nIMPP;TYPE=work:say:200:cafe::3\nEND:VCARD\n"

func write(t *testing.T, dir, name, data string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "alice.vcf", aliceCard)
	write(t, dir, "export.vcf", exportCards)
	write(t, dir, "notes.txt", "not a card")
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.People) != 2 {
		t.Fatalf("%d people, want Alice and Carol", len(b.People))
	}
	a, c := b.People[0], b.People[1]
	if a.Name() != "Alice Ivanova" || a.Given != "Alice" || a.Family != "Ivanova" || a.Middle != "Petrovna" || a.UID != "AAAA-1" {
		t.Errorf("Alice read as %+v", a)
	}
	want := []Device{{"home", "200:abcd::1", 7777}, {"phone", "201:1::2", 7001}}
	if len(a.Devices) != 2 || a.Devices[0] != want[0] || a.Devices[1] != want[1] {
		t.Errorf("Alice's devices %+v, want %+v", a.Devices, want)
	}
	if a.Devices[0].Shown() != "200:abcd::1" || a.Devices[1].Shown() != "[201:1::2]:7001" {
		t.Errorf("shown as %q and %q", a.Devices[0].Shown(), a.Devices[1].Shown())
	}
	if c.Name() != "Carol" || len(c.Devices) != 1 || c.Devices[0] != (Device{"work", "200:cafe::3", 7777}) {
		t.Errorf("Carol read as %+v %+v", c, c.Devices)
	}
	if p, d := b.ByHost("[201:1:0::2]"); p != a || d == nil || d.Label != "phone" {
		t.Errorf("ByHost: %v %v", p, d)
	}
}

// Saving a card Say did not write keeps its other lines (the photo, the
// phone, the URL with its label) and rewrites only the names and devices.
func TestSaveKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "alice.vcf", aliceCard)
	b, _ := Load(dir)
	a := b.People[0]
	a.Devices = append(a.Devices[:1], Device{Label: "laptop", Host: "202::5", Port: 7777})
	a.Given = "Alisa"
	if err := b.Save(a); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	for _, keep := range []string{"TEL;type=CELL", "PHOTO;ENCODING=b", "item3.URL:https://example.org", "item3.X-ABLabel:_$!<HomePage>!$_", "UID:AAAA-1"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q:\n%s", keep, s)
		}
	}
	if strings.Contains(s, "201:1::2") || strings.Contains(s, "label:phone") {
		t.Errorf("the removed device stayed:\n%s", s)
	}
	if photo := strings.Join(unfold(data), "\n"); !strings.Contains(photo, "ICAgMCAgIDAwMDBAYEBAQEBAgGBgUGCQgKCgkICQkKDA8MCgsOCwkJDRENDg8QEBEQCgwSExIQEw8QEBD/") {
		t.Errorf("the photo changed:\n%s", photo)
	}
	b2, _ := Load(dir)
	if len(b2.People) != 1 {
		t.Fatalf("%d people after the save", len(b2.People))
	}
	a2 := b2.People[0]
	if a2.Name() != "Alisa Petrovna Ivanova" || len(a2.Devices) != 2 || a2.Devices[1] != (Device{"laptop", "202::5", 7777}) {
		t.Errorf("read back as %q %+v", a2.Name(), a2.Devices)
	}
	if a2.Devices[0].Label != "home" {
		t.Errorf("home's label %q", a2.Devices[0].Label)
	}
}

// A new person gets a file of their own; removing them deletes it. A card
// in a shared file goes alone.
func TestNewAndRemove(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "export.vcf", exportCards)
	b, _ := Load(dir)
	p := &Person{Given: "Dave", Family: "Moroz", Devices: []Device{{Label: "work", Host: "200::9", Port: 7777}}}
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dave Moroz.vcf")); err != nil {
		t.Fatal(err)
	}
	b2, _ := Load(dir)
	if b2.ByName("dave moroz") == nil || b2.ByName("Dave Moroz").UID == "" {
		t.Fatalf("Dave not read back: %+v", b2.People)
	}
	if err := b2.Remove(b2.ByName("Dave Moroz")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dave Moroz.vcf")); !os.IsNotExist(err) {
		t.Errorf("Dave's file stayed: %v", err)
	}
	if err := b2.Remove(b2.ByName("Carol")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "export.vcf"))
	if !strings.Contains(string(data), "Bob Smith") || strings.Contains(string(data), "Carol") {
		t.Errorf("export after removing Carol:\n%s", data)
	}
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]Device{
		"200:abcd::1":              {Host: "200:abcd::1", Port: 7777},
		"[200:abcd::1]":            {Host: "200:abcd::1", Port: 7777},
		"[200:abcd::1]:7001":       {Host: "200:abcd::1", Port: 7001},
		" say://[200:abcd::1]:70 ": {Host: "200:abcd::1", Port: 70},
	} {
		h, p, err := ParseAddr(in)
		if err != nil || h != want.Host || p != want.Port {
			t.Errorf("ParseAddr(%q) = %q %d %v", in, h, p, err)
		}
	}
	for _, bad := range []string{"", "alice", "[200::1]:x", "[200::1]:70000"} {
		if _, _, err := ParseAddr(bad); err == nil {
			t.Errorf("ParseAddr(%q) took it", bad)
		}
	}
}

// Long lines are folded at 75 octets and never inside a character.
func TestFold(t *testing.T) {
	var b strings.Builder
	writeLine(&b, "NOTE:"+strings.Repeat("я", 100))
	for _, l := range strings.Split(strings.TrimSuffix(b.String(), "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Errorf("a line of %d octets", len(l))
		}
	}
	got := unfold([]byte(b.String()))
	if len(got) < 1 || got[0] != "NOTE:"+strings.Repeat("я", 100) {
		t.Errorf("unfolded to %q", got)
	}
}

// A folder of cards with photos reads at once (it took minutes when each
// folded line joined the whole value again), and a quoted-printable value
// is joined across its soft breaks.
func TestLoadBigPhotos(t *testing.T) {
	dir := t.TempDir()
	photo := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 6000) // ~200 KB
	var b strings.Builder
	writeLine(&b, "PHOTO;ENCODING=b;TYPE=JPEG:"+photo)
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:P\r\n" + b.String() +
		"item1.IMPP;X-SERVICE-TYPE=Say:say://[200::1]\r\nEND:VCARD\r\n"
	for i := 0; i < 100; i++ {
		write(t, dir, fmt.Sprintf("p%d.vcf", i), card)
	}
	start := time.Now()
	bk, err := Load(dir)
	if err != nil || len(bk.People) != 100 {
		t.Fatalf("%v, %d people", err, len(bk.People))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("100 cards with photos took %v", d)
	}
	got := unfold([]byte("NOTE;ENCODING=QUOTED-PRINTABLE:a=3Db=\r\nc\r\nFN:x\r\n"))
	if len(got) != 2 || got[0] != "NOTE;ENCODING=QUOTED-PRINTABLE:a=3Dbc" || got[1] != "FN:x" {
		t.Fatalf("quoted-printable unfolded to %q", got)
	}
}

// A device is written as a link (say:[host]:port, its label in the group:
// Contacts shows it under the label) and read back with it.
func TestWriteSayAddress(t *testing.T) {
	dir := t.TempDir()
	b, _ := Load(dir)
	p := &Person{Given: "Я", Devices: []Device{{Label: "ноутбук", Host: "200::1", Port: 7000}, {Label: "телефон", Host: "201::2", Port: 7777}}}
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p.file)
	s := strings.Join(unfold(data), "\n")
	for _, want := range []string{
		"item1.URL:say:[200::1]:7000", "item1.X-ABLabel:ноутбук",
		"item2.URL:say:[201::2]\n", "item2.X-ABLabel:телефон",
	} {
		if !strings.Contains(s+"\n", want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	b2, _ := Load(dir)
	if got := b2.People[0].Devices; len(got) != 2 || got[0] != p.Devices[0] || got[1] != p.Devices[1] {
		t.Fatalf("read back as %+v", got)
	}
	if d, ok := sayIM(osIM{Label: "ноутбук", Service: "Say", User: "[200::1]:7000"}); !ok || d != p.Devices[0] {
		t.Fatalf("the system's book's address read as %+v", d)
	}
}

// A card with the same address twice (an old line beside a new one) has
// the device once, with the label one of them has.
func TestSameAddressOnce(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "me.vcf", "BEGIN:VCARD\nVERSION:3.0\nFN:Me\n"+
		"IMPP;X-SERVICE-TYPE=Say:say://[200::1]:7000\n"+
		"item1.URL:say:[200::1]:7000\nitem1.X-ABLabel:ноутбук\nEND:VCARD\n")
	b, _ := Load(dir)
	if ds := b.People[0].Devices; len(ds) != 1 || ds[0].Label != "ноутбук" {
		t.Fatalf("devices %+v", ds)
	}
	p := b.People[0]
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p.file)
	if strings.Contains(string(data), "IMPP") || strings.Count(string(data), "say:[200::1]:7000") != 1 {
		t.Fatalf("after the save:\n%s", data)
	}
}

// The same address on another port (an old line, another run) is the same
// Say: one device.
func TestSameHostOtherPort(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "me.vcf", "BEGIN:VCARD\nVERSION:3.0\nFN:Me\n"+
		"item1.URL:say:[200::1]:7000\nitem1.X-ABLabel:тест\n"+
		"item2.URL:say:[200::1]\nitem2.X-ABLabel:ноутбук\nEND:VCARD\n")
	b, _ := Load(dir)
	if ds := b.People[0].Devices; len(ds) != 1 || ds[0].Label != "тест" || ds[0].Port != 7000 {
		t.Fatalf("devices %+v", ds)
	}
}

func TestDisplayStyles(t *testing.T) {
	p := &Person{Given: "Сергей", Middle: "Сергеевич", Family: "Ваничкин", Nick: "alien"}
	for style, want := range map[string]string{ShowGivenFamily: "Сергей Ваничкин", ShowFamilyGiven: "Ваничкин Сергей", ShowShort: "С.С.Ваничкин", ShowNick: "alien"} {
		if got := p.Display(style); got != want {
			t.Errorf("%q: %q, want %q", style, got, want)
		}
	}
	co := &Person{fn: "Apple"}
	if co.Display(ShowShort) != "Apple" || co.Display(ShowNick) != "Apple" {
		t.Errorf("a company: %q", co.Display(ShowShort))
	}
	dir := t.TempDir()
	b, _ := Load(dir)
	if err := b.Save(&Person{Given: "A", Nick: "a, b"}); err != nil {
		t.Fatal(err)
	}
	b, _ = Load(dir)
	if b.All[0].Nick != "a, b" {
		t.Errorf("the nickname read back as %q", b.All[0].Nick)
	}
}

// A card with only FN (no name parts) keeps its name when Say writes it.
func TestSaveKeepsFN(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "g.vcf", "BEGIN:VCARD\nVERSION:3.0\nUID:G\nFN:mr.Geek\nEND:VCARD\n")
	b, _ := Load(dir)
	p := b.All[0].Clone()
	p.Devices = []Device{{Label: "ноутбук", Host: "200::1", Port: 7777}}
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ = Load(dir)
	if b.All[0].Name() != "mr.Geek" {
		t.Fatalf("the name became %q", b.All[0].Name())
	}
}

// The "me" mark: read back, kept when Say rewrites the file, moved by SetMe.
func TestMeMark(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "alice.vcf", aliceCard)
	write(t, dir, "export.vcf", exportCards)
	b, _ := Load(dir)
	if err := b.SetMe(b.ByName("Alice Ivanova")); err == errNoAttr {
		t.Skip("no extended attributes here")
	} else if err != nil {
		t.Fatal(err)
	}
	if b, _ = Load(dir); b.Me != "AAAA-1" {
		t.Fatalf("the mark read back as %q", b.Me)
	}
	a := b.ByUID("AAAA-1").Clone()
	a.Given = "Alisa"
	if err := b.Save(a); err != nil {
		t.Fatal(err)
	}
	if b, _ = Load(dir); b.Me != "AAAA-1" {
		t.Fatalf("Say's rewrite lost the mark: %q", b.Me)
	}
	carol := b.ByName("Carol").Clone()
	if err := b.Save(carol); err != nil { // gives Carol a UID
		t.Fatal(err)
	}
	b, _ = Load(dir)
	if err := b.SetMe(b.ByUID(carol.UID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := getMe(filepath.Join(dir, "alice.vcf")); ok {
		t.Fatal("Alice's file is still marked")
	}
	if b, _ = Load(dir); b.Me != carol.UID {
		t.Fatalf("the mark moved to %q, want %q", b.Me, carol.UID)
	}
}

// A card just written is in the book at once (the system's book is read
// again only later): found by its UID, in place of the one it was.
func TestPutFilesWrittenCard(t *testing.T) {
	old := &Person{UID: "a", Given: "Old"}
	b := &Book{}
	b.add(old)
	seen := b.All
	b.put(&Person{UID: "a", Given: "New", Devices: []Device{{Host: "200::1", Port: 7000}}})
	b.put(&Person{UID: "b", Given: "Other"})
	if p := b.ByUID("a"); p == nil || p.Given != "New" {
		t.Fatalf("a = %+v", p)
	}
	if b.ByUID("b") == nil || len(b.All) != 2 || len(b.People) != 1 {
		t.Fatalf("all %d, people %d", len(b.All), len(b.People))
	}
	if seen[0] != old {
		t.Fatal("the list read before was changed")
	}
}

// A card with only a nickname is shown by it, in every style, and written
// with it as its FN and file name.
func TestNickOnlyCard(t *testing.T) {
	p := &Person{Nick: "SSS"}
	for _, st := range []string{ShowGivenFamily, ShowFamilyGiven, ShowShort, ShowNick} {
		if got := p.Display(st); got != "SSS" {
			t.Fatalf("style %q: %q", st, got)
		}
	}
	dir := t.TempDir()
	b := &Book{Dir: dir}
	if err := b.Save(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "SSS.vcf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "FN:SSS") {
		t.Fatalf("no FN:\n%s", data)
	}
}
