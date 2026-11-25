package contacts

import (
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// A call's two sides send each other their own cards (only what a contact
// needs: the names, the nickname, the devices with their labels). A card
// from an address already in the book is that person's, for sure (an
// address of the mesh comes from its key): what it adds is written to
// their card, nothing written over. A card from an address the book does
// not have waits among the pending ones, Say's own (not the book), until
// it is confirmed: anyone may say they are anyone.

// Pending is a card received in a call from an address not in the book.
type Pending struct {
	*Person
	From string // the address it came from (the device that called)
	file string
}

// MyCard is our own card as sent in a call (nil: we have none).
func (s *Store) MyCard() []byte {
	me := s.Snapshot().Me
	if me == nil {
		return nil
	}
	c := &card{}
	c.set("VERSION", "3.0")
	p := &Person{Given: me.Given, Family: me.Family, Middle: me.Middle, Nick: me.Nick, fn: me.Name(), c: c, Devices: me.Devices}
	writeNames(c, p)
	writeDevices(c, p)
	var b strings.Builder
	c.write(&b)
	return []byte(b.String())
}

// maxPending is how many received cards wait at most: calls from many
// addresses do not fill the disk.
const maxPending = 50

// parseShared reads a card received in a call: its names and labels made
// plain text (no control characters: a terminal draws them; not too long).
func parseShared(data []byte) (*Person, error) {
	cards := parseCards(data)
	if len(cards) == 0 {
		return nil, errors.New("no card")
	}
	p := personOf(cards[0], "")
	p.c, p.UID = nil, ""
	p.Given, p.Family, p.Middle, p.Nick, p.fn = plain(p.Given), plain(p.Family), plain(p.Middle), plain(p.Nick), plain(p.fn)
	for i := range p.Devices {
		p.Devices[i].Label = plain(p.Devices[i].Label)
	}
	return p, nil
}

// plain is a received text without control characters, at most 64 runes.
func plain(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			continue
		}
		if n++; n > 64 {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// Received takes the card a call's peer sent from the address from, and
// returns its name as the list shows it (and the calling device's label)
// for the screen.
func (s *Store) Received(from string, data []byte) (name, label string) {
	p, err := parseShared(data)
	if err != nil || (p.Name() == "" && len(p.Devices) == 0) {
		return "", ""
	}
	from = strings.Trim(from, "[]")
	sent := false // the card has the device it came from
	for _, d := range p.Devices {
		if sameHost(d.Host, from) {
			label, sent = d.Label, true
		}
	}
	s.mu.Lock()
	known, _ := s.book.ByHost(from)
	display := s.settings.Display
	if known != nil {
		// Theirs for sure: what they add is written, nothing over.
		if _, err := s.updateLocked(known.UID, func(q *Person) { s.mergeLocked(q, p) }); err != nil {
			log.Printf("[contacts] a known card's additions: %v", err)
		}
		s.readLocked()
		if q, _ := s.book.ByHost(from); q != nil {
			known = q
		}
		s.mu.Unlock()
		return known.Display(display), label
	}
	full := len(s.pending) >= maxPending
	s.mu.Unlock()
	name = p.Display(display)
	if !sent || full {
		return name, label // not kept: not its own device's card, or too many waiting
	}
	if err := s.savePending(from, data); err != nil {
		log.Printf("[contacts] a received card not kept: %v", err)
		return name, label
	}
	s.mu.Lock()
	s.pending = s.loadPending()
	s.publishLocked()
	s.mu.Unlock()
	return name, label
}

// mergeLocked adds to q what p has and q lacks: the name where q has none
// at all, the nickname, labels of devices q has without, devices at
// addresses no card in the book has.
func (s *Store) mergeLocked(q, p *Person) {
	if q.Name() == "" { // no name at all (a card with only FN has one)
		q.Given, q.Family, q.Middle = p.Given, p.Family, p.Middle
	}
	if q.Nick == "" {
		q.Nick = p.Nick
	}
	for _, d := range p.Devices {
		if q.Has(d.Host) {
			for i := range q.Devices {
				if q.Devices[i].Host == d.Host && q.Devices[i].Label == "" {
					q.Devices[i].Label = d.Label
				}
			}
			continue
		}
		if other, _ := s.book.ByHost(d.Host); other != nil {
			continue // someone else's in the book: not taken
		}
		q.Devices = append(q.Devices, d)
	}
}

func sameHost(a, b string) bool {
	if a == b {
		return true
	}
	ia, ib := net.ParseIP(a), net.ParseIP(b)
	return ia != nil && ib != nil && ia.Equal(ib)
}

// pendingDir is where pending cards wait: beside the settings, Say's own.
func (s *Store) pendingDir() string {
	return strings.TrimSuffix(s.settings.path, filepath.Ext(s.settings.path)) + "-pending"
}

// savePending keeps a pending card, one per address (the latest). The file
// is named by the address, ":" as "_": an address is always an IP address
// (the connection's, or ParseAddr's), so nothing else gets into the name.
func (s *Store) savePending(from string, data []byte) error {
	dir := s.pendingDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if net.ParseIP(from) == nil {
		return errors.New("not an address")
	}
	name := strings.ReplaceAll(from, ":", "_") + ".vcf"
	return os.WriteFile(filepath.Join(dir, name), data, 0o600)
}

// loadPending reads the pending cards (on start and when they change: the
// snapshots share the list).
func (s *Store) loadPending() []*Pending {
	entries, _ := os.ReadDir(s.pendingDir())
	var out []*Pending
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".vcf") {
			continue
		}
		path := filepath.Join(s.pendingDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		p, err := parseShared(data)
		if err != nil {
			continue
		}
		from := strings.ReplaceAll(strings.TrimSuffix(e.Name(), ".vcf"), "_", ":")
		out = append(out, &Pending{Person: p, From: from, file: path})
	}
	return out
}

// Confirm takes a pending card into the book: as a new card (into "") or
// added to the card into (what it adds; nothing written over).
func (s *Store) Confirm(pd *Pending, into string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := pd.Person
	_, err := s.updateLocked(into, func(q *Person) {
		if into == "" {
			q.Given, q.Family, q.Middle, q.Nick = p.Given, p.Family, p.Middle, p.Nick
			if q.Given == "" && q.Family == "" && p.Name() != "" {
				q.Given = p.Name()
			}
		}
		s.mergeLocked(q, p)
	})
	if err == nil {
		_ = os.Remove(pd.file)
		s.pending = s.loadPending()
	}
	s.readLocked()
	return err
}

// Reject drops a pending card.
func (s *Store) Reject(pd *Pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(pd.file)
	s.pending = s.loadPending()
	s.publishLocked()
}

// SameName are the cards in the book with the pending card's name (first
// and last, and middle where both have one): where it may be added.
func (sn *Snapshot) SameName(p *Person) []*Person {
	var out []*Person
	key := func(q *Person) string {
		return strings.ToLower(strings.TrimSpace(q.Given) + " " + strings.TrimSpace(q.Family))
	}
	if strings.TrimSpace(key(p)) == "" {
		return nil
	}
	for _, q := range sn.All {
		if key(q) == key(p) && (q.Middle == "" || p.Middle == "" || strings.EqualFold(q.Middle, p.Middle)) {
			out = append(out, q)
		}
	}
	return out
}
