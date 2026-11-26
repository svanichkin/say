package contacts

import (
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store is the address book Say works with. It is read once and kept as a
// Snapshot; it is read again only after Say writes to it and when the
// system tells of a change (the folder's files, the system's book). All
// reading and writing is here: the menu only reads snapshots and asks for
// changes.
//
// Our card is, in a folder, the one whose file is marked "me" (meattr.go);
// in the system's book, the one it has as ours ("My Card"), unless another
// was chosen in Say. This Say's address is put on our card, labelled with
// the device's kind.
type Store struct {
	mu       sync.Mutex // the book, the settings, writes and reads
	book     *Book      // as read (People and All are not shared with snapshots)
	settings *Settings
	self     string          // this Say's address ("" until the node has it)
	port     int             // the port it listens on
	kind     string          // this device's label when it is put on a card: its kind
	loads    int             // the read of the system's book in flight (a newer one wins)
	ready    bool            // read at least once
	sysMe    string          // the system's own card for us ("My Card")
	pending  []*Pending      // the cards waiting to be confirmed (read on start and on change)
	ensured  map[string]bool // our cards this Say's address was put on this run (once each)
	madeMe   bool            // a card of ours was made this run (none was there)

	snap    atomic.Pointer[Snapshot]
	changed func() // a new snapshot
	unwatch func()
	settle  *time.Timer // changes told come in bursts: read once they settle
}

// Snapshot is the address book at a moment, not to be changed: the menu
// draws from it.
type Snapshot struct {
	Ready    bool   // read (the system's book takes a moment the first time)
	Source   string // "os": the system's book; else the folder
	Dir      string
	Display  string
	Last     string
	LastHost map[string]string
	Self     string
	Port     int
	Kind     string     // this device's label by its kind
	All      []*Person  // every card, by name
	People   []*Person  // with Say addresses, but ours
	Me       *Person    // our card (nil: none)
	Pending  []*Pending // cards received in calls from addresses not in the book
	// Chosen: the source was chosen by the user (else it is the default).
	// NeedAccess: the default is the system's book, its access not asked
	// for yet (AskAccess, when the contacts are first opened).
	Chosen, NeedAccess bool
}

// NewStore opens the address book the settings at path name (dir, if set,
// as its folder from now on). kind is this device's label when Say puts it
// on a card; changed is called after every new snapshot.
func NewStore(path, dir, kind string, port int, changed func()) *Store {
	st, err := LoadSettings(path)
	if err != nil {
		log.Printf("[contacts] settings: %v", err)
	}
	if dir != "" && dir != st.Dir {
		st.Dir, st.Source = dir, ""
		if err := st.Save(); err != nil {
			log.Printf("[contacts] settings: %v", err)
		}
	}
	s := &Store{settings: st, port: port, kind: kind, changed: changed, book: &Book{}}
	s.pending = s.loadPending()
	s.mu.Lock()
	s.readLocked()
	s.watchLocked()
	s.mu.Unlock()
	return s
}

// Snapshot is the address book now.
func (s *Store) Snapshot() *Snapshot {
	if sn := s.snap.Load(); sn != nil {
		return sn
	}
	return &Snapshot{}
}

// SetSelf is this Say's address, once the node has it: it goes on our card.
func (s *Store) SetSelf(addr string) {
	s.mu.Lock()
	s.self = addr
	s.publishLocked()
	s.ensureLocked()
	s.mu.Unlock()
}

// meLocked is our card's UID: the file marked "me" (a folder); the one
// chosen, else the system's own (the system's book).
func (s *Store) meLocked() string {
	if s.sourceLocked() != "os" {
		return s.book.Me
	}
	if s.settings.Me != "" && s.book.ByUID(s.settings.Me) != nil {
		return s.settings.Me
	}
	return s.sysMe
}

// ensureLocked puts this Say's address on our card when it lacks it (a
// card made or marked elsewhere), once a run per card: a card that does not
// keep it is not written over and over. In a folder with no file marked
// "me" (the mark lost: another program rewrote the file), the one card
// with this Say's address, which Say put there, is marked again; with no
// such card either, one is made (the computer's name, this device).
func (s *Store) ensureLocked() {
	if s.sourceLocked() != "os" && s.book.Me == "" && s.self != "" && s.ready {
		var ours []*Person
		for _, p := range s.book.All {
			if p.Has(s.self) {
				ours = append(ours, p)
			}
		}
		if len(ours) == 1 && s.book.SetMe(ours[0]) == nil {
			log.Printf("[contacts] our card's file marked me again: %s", ours[0].Name())
			s.publishLocked()
		}
		if len(ours) == 0 && !s.madeMe {
			// No card of ours at all (a first start): one is made, the
			// computer's name its nickname, this device on it; it is
			// sent in calls and can be renamed as any.
			s.madeMe = true
			uid, err := s.updateLocked("", func(p *Person) {
				p.Nick = hostName()
				s.putHere(p)
			})
			if err == nil {
				err = s.takeAsMeLocked(uid)
			}
			if err != nil {
				log.Printf("[contacts] our card not made: %v", err)
			} else {
				log.Printf("[contacts] our card made: %s", hostName())
			}
			s.publishLocked()
		}
	}
	uid := s.meLocked()
	p := s.book.ByUID(uid)
	if p == nil || s.self == "" || !s.ready || p.Has(s.self) || s.ensured[uid] {
		return
	}
	if s.ensured == nil {
		s.ensured = map[string]bool{}
	}
	s.ensured[uid] = true
	log.Printf("[contacts] this device put on our card")
	if _, err := s.updateLocked(uid, s.putHere); err == nil {
		s.readLocked()
	}
}

// publishLocked makes a snapshot of what is read and tells of it.
func (s *Store) publishLocked() {
	sn := &Snapshot{
		Ready: s.ready, Source: s.sourceLocked(), Dir: s.dirLocked(), Display: s.settings.Display,
		Chosen: s.settings.Source != "" || s.settings.Dir != "", NeedAccess: s.needAccessLocked(),
		Last: s.settings.Last, LastHost: map[string]string{},
		Self: s.self, Port: s.port, Kind: s.kind,
		All:     append([]*Person(nil), s.book.All...),
		Pending: s.pending,
	}
	for k, v := range s.settings.LastHost {
		sn.LastHost[k] = v
	}
	me := s.meLocked()
	for _, p := range sn.All {
		switch {
		case me != "" && p.UID == me:
			sn.Me = p
		case len(p.Devices) > 0:
			sn.People = append(sn.People, p)
		}
	}
	s.snap.Store(sn)
	if s.changed != nil {
		go s.changed()
	}
}

// Has reports whether the person has a device at host.
func (p *Person) Has(host string) bool {
	for _, d := range p.Devices {
		if d.Host == host {
			return true
		}
	}
	return false
}

// readLocked reads the book where the contacts are (sourceLocked): the
// folder at once, the system's book aside (seconds the first time), then
// publishes.
func (s *Store) readLocked() {
	switch {
	case s.sourceLocked() == "os" && s.needAccessLocked():
		// The system's book by default, not asked for yet: asked when the
		// contacts are first opened (AskAccess), not at start.
		s.book, s.ready = SystemBook(), true
		s.publishLocked()
	case s.sourceLocked() == "os":
		s.loads++
		gen := s.loads
		if !s.book.os {
			s.book, s.ready, s.sysMe = SystemBook(), false, ""
		}
		s.publishLocked()
		go s.readSystem(gen)
	default:
		b, err := Load(s.dirLocked())
		if err != nil {
			log.Printf("[contacts] the folder: %v", err)
		}
		s.book, s.ready = b, true
		s.publishLocked()
		s.ensureLocked()
	}
}

// readSystem reads the system's book, then the card it has as ours
// (Contacts takes seconds for it: the cards are shown meanwhile).
func (s *Store) readSystem(gen int) {
	start := time.Now()
	b, err := LoadOS()
	if err != nil {
		log.Printf("[contacts] the system's address book: %v", err)
	}
	log.Printf("[contacts] the system's address book: %d cards, %d with Say, read in %v (%s)", len(b.All), len(b.People), time.Since(start).Round(time.Millisecond), b.Timing)
	s.mu.Lock()
	if gen != s.loads || s.sourceLocked() != "os" {
		s.mu.Unlock()
		return
	}
	s.book, s.ready = b, true
	s.publishLocked()
	s.mu.Unlock()
	start = time.Now()
	id, err := SystemMe()
	if err != nil {
		return
	}
	log.Printf("[contacts] the system's own card for us read in %v", time.Since(start).Round(time.Millisecond))
	s.mu.Lock()
	if gen == s.loads {
		s.sysMe = id
		s.publishLocked()
		s.ensureLocked()
	}
	s.mu.Unlock()
}

// watchLocked follows changes to the book from outside Say: the folder's
// files (the system's notifications), the system's book (its own).
func (s *Store) watchLocked() {
	if s.unwatch != nil {
		s.unwatch()
		s.unwatch = nil
	}
	reread := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.settle != nil {
			s.settle.Stop()
		}
		s.settle = time.AfterFunc(300*time.Millisecond, func() {
			s.mu.Lock()
			s.readLocked()
			s.mu.Unlock()
		})
	}
	switch {
	case s.sourceLocked() == "os":
		s.unwatch = watchSystem(reread)
	default:
		// A folder not made yet is followed once a card is written to it
		// (updateLocked).
		if _, err := os.Stat(s.dirLocked()); err != nil {
			return
		}
		stop, err := watchFolder(s.dirLocked(), reread)
		if err != nil {
			log.Printf("[contacts] changes to the folder are not followed: %v", err)
		}
		s.unwatch = stop
	}
}

// needAccessLocked: the system's book is where the contacts are by
// default, and the user was not asked for it yet.
func (s *Store) needAccessLocked() bool {
	return s.settings.Source == "" && s.settings.Dir == "" && OSBookAvailable() && OSBookStatus() == NotAsked
}

// AskAccess asks the user for the system's book (the contacts opened the
// first time with it the default): read when granted; refused, the folder
// by default is where the contacts are.
func (s *Store) AskAccess() {
	s.mu.Lock()
	need := s.needAccessLocked()
	s.mu.Unlock()
	if !need {
		return
	}
	OSBookAccess() // the system asks (and waits for the user)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readLocked()
	s.watchLocked()
}

// UseFolder makes dir (made if missing) where the contacts come from.
func (s *Store) UseFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Dir, s.settings.Source = dir, ""
	if err := s.settings.Save(); err != nil {
		return err
	}
	s.readLocked()
	s.watchLocked()
	return nil
}

// UseSystem makes the system's book where the contacts come from (the
// system asks the user for access the first time).
func (s *Store) UseSystem() error {
	if !OSBookAccess() {
		return ErrNoAccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Source = "os"
	if err := s.settings.Save(); err != nil {
		return err
	}
	s.readLocked()
	s.watchLocked()
	return nil
}

// SetDisplay is how people are named in the list.
func (s *Store) SetDisplay(style string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Display = style
	if err := s.settings.Save(); err != nil {
		log.Printf("[contacts] settings: %v", err)
	}
	s.publishLocked()
}

// Noted records a call with host: its person comes first next time, in
// that device's colour.
func (s *Store) Noted(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, _ := s.book.ByHost(host); p != nil {
		s.settings.Noted(p.UID, host)
		if err := s.settings.Save(); err != nil {
			log.Printf("[contacts] settings: %v", err)
		}
		s.publishLocked()
	}
}

// NameOf is the name of whoever has a device at host ("": nobody).
func (s *Store) NameOf(host string) string {
	if p, _ := s.Snapshot().byHost(host); p != nil {
		return p.Display(s.Snapshot().Display)
	}
	return ""
}

// Lookup is the device to call a person by name on: the one called last,
// else the first.
func (s *Store) Lookup(name string) (Device, bool) {
	sn := s.Snapshot()
	for _, p := range sn.People {
		if !strings.EqualFold(p.Name(), strings.TrimSpace(name)) && !strings.EqualFold(p.Display(sn.Display), strings.TrimSpace(name)) {
			continue
		}
		d := p.Devices[0]
		for _, dd := range p.Devices {
			if dd.Host == sn.LastHost[p.UID] {
				d = dd
			}
		}
		return d, true
	}
	return Device{}, false
}

func (sn *Snapshot) byHost(host string) (*Person, *Device) {
	return (&Book{All: sn.All, People: sn.All}).ByHost(host)
}

// ByUID is the card with that UID in the snapshot (nil: none).
func (sn *Snapshot) ByUID(uid string) *Person {
	if uid == "" {
		return nil
	}
	for _, p := range sn.All {
		if p.UID == uid {
			return p
		}
	}
	return nil
}

// Update changes a card (uid "": a new one) as f says and writes it; the
// book is read again. It returns the card's UID (a new card's, made now).
func (s *Store) Update(uid string, f func(p *Person)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uid, err := s.updateLocked(uid, f)
	s.readLocked()
	return uid, err
}

func (s *Store) updateLocked(uid string, f func(p *Person)) (string, error) {
	p := &Person{}
	if uid != "" {
		q := s.book.ByUID(uid)
		if q == nil {
			return uid, errors.New("no such card")
		}
		p = q.Clone()
	}
	f(p)
	if s.book.Dir == "" && !s.book.os {
		s.book.Dir = s.dirLocked() // the folder by default: made by this write
	}
	if err := s.book.Save(p); err != nil {
		log.Printf("[contacts] a card not written: %v", err)
		return p.UID, err
	}
	s.book.put(p)
	if s.unwatch == nil {
		s.watchLocked() // a folder just made
	}
	return p.UID, nil
}

// TakeAsMe makes the card uid ours: in a folder its file is marked "me"
// (and no other), in the system's book it is kept in the settings; this
// Say's address is put on it (labelled with the device's kind) and taken
// off any other card.
func (s *Store) TakeAsMe(uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeAsMeLocked(uid)
}

func (s *Store) takeAsMeLocked(uid string) error {
	var err error
	if s.self != "" {
		_, err = s.updateLocked(uid, s.putHere)
		for _, q := range s.book.All {
			if q.UID != uid && q.Has(s.self) {
				if _, e := s.updateLocked(q.UID, s.takeHereOff); e != nil && err == nil {
					err = e
				}
			}
		}
	}
	if s.sourceLocked() == "os" {
		s.settings.Me = uid
		if e := s.settings.Save(); e != nil && err == nil {
			err = e
		}
	} else {
		s.readLocked() // the card's file as written now
		if p := s.book.ByUID(uid); p != nil {
			if e := s.book.SetMe(p); e != nil {
				log.Printf("[contacts] our card's file not marked: %v", e)
				if err == nil {
					err = e
				}
			}
		}
	}
	s.readLocked()
	return err
}

// Remove takes a person out of Say: a card Say made (in a folder) is
// deleted; any other, made elsewhere (the system's book, or a folder kept
// with other programs), loses its Say addresses only, the rest of it kept.
func (s *Store) Remove(uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.book.ByUID(uid)
	if p == nil {
		return errors.New("no such card")
	}
	if !s.book.os && p.c != nil && p.c.get("PRODID") == sayProdID {
		if err := s.book.Remove(p); err != nil {
			return err
		}
	} else if _, err := s.updateLocked(uid, func(q *Person) { q.Devices = nil }); err != nil {
		return err
	}
	s.readLocked()
	return nil
}

// CreateMe makes a card of ours (or, uid set, changes the one made): its
// names, this Say's address on it, its file marked "me".
func (s *Store) CreateMe(uid string, names func(p *Person)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uid, err := s.updateLocked(uid, func(p *Person) {
		names(p)
		if s.self != "" {
			s.putHere(p)
		}
	})
	if err != nil {
		return uid, err
	}
	return uid, s.takeAsMeLocked(uid)
}

// SetHereLabel is this device's label on our card (and the port it
// listens on now).
func (s *Store) SetHereLabel(label string) error {
	me := s.Snapshot().Me
	if me == nil {
		return errors.New("no card of ours")
	}
	_, err := s.Update(me.UID, func(p *Person) {
		for i := range p.Devices {
			if p.Devices[i].Host == s.self {
				p.Devices[i].Label, p.Devices[i].Port = label, s.port
				return
			}
		}
	})
	return err
}

// putHere puts this Say's address on a card, unless it is there.
func (s *Store) putHere(p *Person) {
	if !p.Has(s.self) {
		p.Devices = append([]Device{{Label: s.kind, Host: s.self, Port: s.port}}, p.Devices...)
	}
}

// takeHereOff takes this Say's address off a card.
func (s *Store) takeHereOff(p *Person) {
	kept := p.Devices[:0]
	for _, d := range p.Devices {
		if d.Host != s.self {
			kept = append(kept, d)
		}
	}
	p.Devices = kept
}
