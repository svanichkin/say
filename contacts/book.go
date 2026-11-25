package contacts

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultPort is the port Say listens on unless told otherwise; an address
// on it is written without one.
const DefaultPort = 7777

// Device is one of a person's devices: a Say address and its label.
type Device struct {
	Label string // "home", "phone"; "" for none
	Host  string // the Yggdrasil address, without brackets
	Port  int    // DefaultPort when the address names none
}

// Addr is the device's address as Say dials it: [host]:port.
func (d Device) Addr() string { return net.JoinHostPort(d.Host, strconv.Itoa(d.Port)) }

// Shown is the address as the menu shows it: the host, and the port only
// when it is not the default.
func (d Device) Shown() string {
	if d.Port == DefaultPort || d.Port == 0 {
		return d.Host
	}
	return "[" + d.Host + "]:" + strconv.Itoa(d.Port)
}

// uri is the device's address in a card, a link: say:[host]:port (the
// port only when it is not the default).
func (d Device) uri() string { return "say:" + d.user() }

// user is the address as the instant messaging name: [host], with the
// port when it is not the default.
func (d Device) user() string {
	if d.Port == DefaultPort || d.Port == 0 {
		return "[" + d.Host + "]"
	}
	return "[" + d.Host + "]:" + strconv.Itoa(d.Port)
}

// Person is a card with Say addresses (or our own card).
type Person struct {
	UID                   string
	Given, Family, Middle string
	Nick                  string // NICKNAME (Contacts: Nickname)
	Devices               []Device

	fn   string // FN as read
	file string // the file the card is in
	c    *card
}

// Name is the person's name as shown: FN, else what N gives.
func (p *Person) Name() string {
	if p.fn != "" {
		return p.fn
	}
	return joinName(p.Given, p.Middle, p.Family)
}

// Display styles: how people are named in the list.
const (
	ShowGivenFamily = ""       // Sergey Vanichkin (the default)
	ShowFamilyGiven = "family" // Vanichkin Sergey
	ShowShort       = "short"  // S.S.Vanichkin
	ShowNick        = "nick"   // the nickname
)

// Display is the person's name in a style; where the card lacks what the
// style takes, the name as it is (first and last name, the company).
func (p *Person) Display(style string) string {
	var s string
	switch style {
	case ShowNick:
		s = strings.TrimSpace(p.Nick)
	case ShowFamilyGiven:
		s = joinName(p.Family, p.Given)
	case ShowShort:
		if p.Family != "" {
			s = initial(p.Given) + initial(p.Middle) + p.Family
		}
	default:
		s = joinName(p.Given, p.Family)
	}
	if s == "" {
		s = joinName(p.Given, p.Family)
	}
	if s == "" {
		s = p.Name()
	}
	if s == "" {
		s = strings.TrimSpace(p.Nick) // a card with only a nickname
	}
	return s
}

// initial is a name's first letter and a dot ("" for none).
func initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return string(r) + "."
	}
	return ""
}

func joinName(parts ...string) string {
	var kept []string
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, " ")
}

// Book is the people in a folder of cards, or in the system's address
// book (LoadOS), as read.
type Book struct {
	Dir    string
	People []*Person // with Say addresses, by name
	All    []*Person // every card, by name
	Me     string    // our card's UID: its file marked "me" (a folder)
	Timing string    // how long the system's book took to read, by stage (the log)

	os bool // the system's address book
}

// Load reads every .vcf file in dir (not its subfolders). A missing
// folder is an empty book.
func Load(dir string) (*Book, error) {
	b := &Book{Dir: dir}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".vcf") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		cards := parseCards(data)
		for _, c := range cards {
			b.add(personOf(c, path))
		}
		// Our card: the one its file's mark names (the file's first if the
		// mark names none); the first file marked.
		if uid, ok := getMe(path); ok && b.Me == "" && len(cards) > 0 {
			b.Me = cards[0].get("UID")
			for _, c := range cards {
				if uid != "" && c.get("UID") == uid {
					b.Me = uid
				}
			}
		}
	}
	b.sort()
	return b, nil
}

// SetMe marks p's file "me" (p's UID), and no other file in the folder.
func (b *Book) SetMe(p *Person) error {
	if b.os || p.file == "" {
		return errNoAttr
	}
	if err := setMe(p.file, p.UID); err != nil {
		return err
	}
	entries, _ := os.ReadDir(b.Dir)
	for _, e := range entries {
		path := filepath.Join(b.Dir, e.Name())
		if path != p.file && strings.EqualFold(filepath.Ext(e.Name()), ".vcf") {
			if _, ok := getMe(path); ok {
				_ = clearMe(path)
			}
		}
	}
	b.Me = p.UID
	return nil
}

// add files a person read: every card in All, those with Say in People.
func (b *Book) add(p *Person) {
	b.All = append(b.All, p)
	if len(p.Devices) > 0 {
		b.People = append(b.People, p)
	}
}

// put files a person just written in place of the card with its UID (or
// as a new one): the book as written, before it is read again (the
// system's book is read again only when it tells of the change). The
// lists are made anew: those read before stay as they were.
func (b *Book) put(p *Person) {
	all := b.All
	b.All, b.People = nil, nil
	placed := false
	for _, q := range all {
		if q.UID == p.UID {
			q, placed = p, true
		}
		b.add(q)
	}
	if !placed {
		b.add(p)
	}
}

// Clone is a copy of the person to change and save: the card read stays as
// it was (the snapshot others read).
func (p *Person) Clone() *Person {
	q := *p
	q.Devices = append([]Device(nil), p.Devices...)
	if p.c != nil {
		q.c = &card{props: append([]prop(nil), p.c.props...)}
	}
	return &q
}

func (b *Book) sort() {
	for _, l := range [][]*Person{b.People, b.All} {
		sort.SliceStable(l, func(i, j int) bool { return strings.ToLower(l[i].Name()) < strings.ToLower(l[j].Name()) })
	}
}

// ByUID is the person with that UID (nil: none).
func (b *Book) ByUID(uid string) *Person {
	if uid == "" {
		return nil
	}
	for _, p := range b.All {
		if p.UID == uid {
			return p
		}
	}
	return nil
}

// ByHost is the person with a device at host, and that device.
func (b *Book) ByHost(host string) (*Person, *Device) {
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	for _, p := range b.People {
		for i := range p.Devices {
			d := &p.Devices[i]
			if strings.EqualFold(d.Host, host) || (ip != nil && ip.Equal(net.ParseIP(d.Host))) {
				return p, d
			}
		}
	}
	return nil, nil
}

// ByName is the person called name (case-insensitive; nil: none).
func (b *Book) ByName(name string) *Person {
	for _, p := range b.People {
		if strings.EqualFold(p.Name(), strings.TrimSpace(name)) {
			return p
		}
	}
	return nil
}

// personOf reads a card.
func personOf(c *card, file string) *Person {
	p := &Person{UID: c.get("UID"), fn: c.get("FN"), file: file, c: c}
	if n := splitList(rawOf(c, "NICKNAME")); len(n) > 0 {
		p.Nick = n[0]
	}
	if n := c.get("N"); n != "" {
		parts := splitValue(rawOf(c, "N"))
		at := func(i int) string {
			if i < len(parts) {
				return strings.TrimSpace(parts[i])
			}
			return ""
		}
		p.Family, p.Given, p.Middle = at(0), at(1), at(2)
	}
	labels := map[string]string{}
	for _, pr := range c.props {
		if pr.name == "X-ABLABEL" && pr.group != "" {
			labels[strings.ToLower(pr.group)] = abLabel(unescape(pr.value))
		}
	}
	for _, pr := range c.props {
		d, ok := deviceOf(pr)
		if !ok {
			continue
		}
		d.Label = labels[strings.ToLower(pr.group)]
		if d.Label == "" {
			d.Label = typeLabel(pr.param("TYPE"))
		}
		p.Devices = addDevice(p.Devices, d)
	}
	return p
}

// addDevice adds d unless its address is there already (a card written
// twice, an old line beside a new one; one Yggdrasil address is one Say,
// whatever the port): the first stays, with a label if one of them has it.
func addDevice(ds []Device, d Device) []Device {
	for i, o := range ds {
		if o.Host == d.Host {
			if o.Label == "" {
				ds[i].Label = d.Label
			}
			return ds
		}
	}
	return append(ds, d)
}

// rawOf is the first property's value as written (escapes kept).
func rawOf(c *card, name string) string {
	for _, p := range c.props {
		if p.name == name {
			return p.value
		}
	}
	return ""
}

// deviceOf reads a Say address from a URL property (say:[host]:port, as
// Say writes it), or an instant messaging one (as Say wrote it before):
// say:[host]:port, say://[host]:port, say:host.
func deviceOf(p prop) (Device, bool) {
	if p.name != "URL" && p.name != "IMPP" && p.name != "X-SAY" {
		return Device{}, false
	}
	v := strings.TrimSpace(unescape(p.value))
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "say://"):
		v = v[len("say://"):]
	case strings.HasPrefix(lower, "say:"):
		v = v[len("say:"):]
	case p.name == "X-SAY":
	default:
		return Device{}, false
	}
	v = strings.TrimSuffix(v, "/")
	host, port, err := ParseAddr(v)
	if err != nil {
		return Device{}, false
	}
	return Device{Host: host, Port: port}, true
}

// ParseAddr reads an address as typed or pasted: [host]:port, [host], or a
// bare IPv6 address; the port is DefaultPort when it names none.
func ParseAddr(s string) (host string, port int, err error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "say://") {
		s = s[len("say://"):]
	}
	s = strings.TrimSuffix(s, "/")
	port = DefaultPort
	switch {
	case strings.HasPrefix(s, "["):
		if h, p, e := net.SplitHostPort(s); e == nil {
			n, e := strconv.Atoi(p)
			if e != nil || n <= 0 || n > 65535 {
				return "", 0, fmt.Errorf("bad port %q", p)
			}
			host, port = h, n
		} else {
			host = strings.Trim(s, "[]")
		}
	default:
		host = s
	}
	if net.ParseIP(host) == nil {
		return "", 0, fmt.Errorf("not an address: %q", s)
	}
	return host, port, nil
}

// abLabel unwraps the labels macOS Contacts writes for its own names
// (_$!<Home>!$_).
func abLabel(s string) string {
	if strings.HasPrefix(s, "_$!<") && strings.HasSuffix(s, ">!$_") {
		return strings.ToLower(s[4 : len(s)-4])
	}
	return s
}

// typeLabel is a label from TYPE=HOME, TYPE=WORK (not the IM kinds).
func typeLabel(t string) string {
	for _, v := range strings.Split(t, ",") {
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case "", "pref", "internet", "x-say", "say":
		default:
			return v
		}
	}
	return ""
}

// Save writes the person's card: a new file named by the name for a new
// person, else the file the card came from, its other cards and the card's
// other lines kept.
func (b *Book) Save(p *Person) error {
	if b.os {
		if err := saveOS(p); err != nil {
			return err
		}
		return nil
	}
	if p.c == nil {
		p.c = &card{props: []prop{{name: "VERSION", value: "3.0"}, {name: "PRODID", value: "-//Say//Say//EN"}}}
	}
	c := p.c
	// The card as the file has it: by its UID, or by its name without one.
	wasUID, wasFN := c.get("UID"), c.get("FN")
	if p.UID == "" {
		p.UID = newUID()
	}
	c.set("UID", p.UID)
	writeNames(c, p)
	writeDevices(c, p)

	if p.file == "" {
		if err := os.MkdirAll(b.Dir, 0o755); err != nil {
			return err
		}
		p.file = freeName(b.Dir, p.Display(ShowGivenFamily))
	}
	return writeFile(p.file, wasUID, wasFN, c)
}

// Remove deletes the person's card (the file when it holds only that card).
func (b *Book) Remove(p *Person) error {
	for i, q := range b.People {
		if q == p {
			b.People = append(b.People[:i], b.People[i+1:]...)
			break
		}
	}
	if p.file == "" {
		return nil
	}
	return writeFile(p.file, p.UID, p.fn, nil)
}

// writeFile writes the file with repl in place of the card that has uid
// (or, without one, the name fn); repl nil removes it. A file left without
// cards is deleted.
func writeFile(path, uid, fn string, repl *card) error {
	var cards []*card
	if data, err := os.ReadFile(path); err == nil {
		cards = parseCards(data)
	}
	found := false
	for i, c := range cards {
		cu := c.get("UID")
		if (uid != "" && cu == uid) || (cu == "" && fn != "" && c.get("FN") == fn) {
			if repl == nil {
				cards = append(cards[:i], cards[i+1:]...)
			} else {
				cards[i] = repl
			}
			found = true
			break
		}
	}
	if !found && repl != nil {
		cards = append(cards, repl)
	}
	if len(cards) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var out strings.Builder
	for _, c := range cards {
		c.write(&out)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out.String()), 0o644); err != nil {
		return err
	}
	if uid, ok := getMe(path); ok {
		_ = setMe(tmp, uid) // the file stays marked "me"
	}
	return os.Rename(tmp, path)
}

// freeName is a file name for a new card: the name, cleaned, and a number
// when that file exists.
func freeName(dir, name string) string {
	base := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if base == "" {
		base = "contact"
	}
	path := filepath.Join(dir, base+".vcf")
	for i := 2; ; i++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s %d.vcf", base, i))
	}
}

// newUID is a random UUID (version 4), as Contacts gives its cards.
func newUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// writeNames writes the person's names on the card: the name from its
// parts (a card with none, only FN, as some programs write, keeps the name
// it has), and the nickname.
func writeNames(c *card, p *Person) {
	if full := joinName(p.Given, p.Middle, p.Family); full != "" {
		p.fn = full
		c.set("FN", escape(p.fn))
		c.set("N", escape(p.Family)+";"+escape(p.Given)+";"+escape(p.Middle)+";;")
	} else if c.get("FN") == "" {
		c.set("FN", escape(p.Display(ShowGivenFamily))) // vCard wants one: the nickname, if all there is
	}
	c.set("NICKNAME", escape(p.Nick))
}

// writeDevices writes the person's Say addresses on the card again, each a
// link in a group of its own with its label (Contacts shows a link under
// its label; an instant messaging address it shows under the service's
// name, the label not shown).
func writeDevices(c *card, p *Person) {
	sayGroups := map[string]bool{}
	for _, pr := range c.props {
		if _, ok := deviceOf(pr); ok && pr.group != "" {
			sayGroups[strings.ToLower(pr.group)] = true
		}
	}
	c.drop(func(pr prop) bool {
		_, say := deviceOf(pr)
		return say || (pr.name == "X-ABLABEL" && sayGroups[strings.ToLower(pr.group)])
	})
	used := map[string]bool{}
	for _, pr := range c.props {
		used[strings.ToLower(pr.group)] = true
	}
	n := 1
	for _, d := range p.Devices {
		for used["item"+strconv.Itoa(n)] {
			n++
		}
		g := "item" + strconv.Itoa(n)
		used[g] = true
		c.props = append(c.props, prop{group: g, name: "URL", value: d.uri()})
		if d.Label != "" {
			c.props = append(c.props, prop{group: g, name: "X-ABLABEL", spelt: "X-ABLabel", value: escape(d.Label)})
		}
	}
}
