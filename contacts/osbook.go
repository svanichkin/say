package contacts

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The system's address book (macOS Contacts): people are read with their
// instant messaging addresses of the service "Say" (say://[host]:port),
// labelled as the card labels them; Say writes only those addresses and the
// names. Our own card is the one the system has as ours ("My Card"), unless
// another is chosen.

// ErrNoAccess: the system did not let Say read its address book.
var ErrNoAccess = errors.New("no access to the address book")

// osPerson is a card as the system gives it.
type osPerson struct {
	ID     string  `json:"id"`
	Given  string  `json:"given"`
	Family string  `json:"family"`
	Middle string  `json:"middle"`
	Nick   string  `json:"nick"`
	Name   string  `json:"name"` // the company, for a card without a name
	IM     []osIM  `json:"im"`   // instant messaging (Say's before)
	URL    []osURL `json:"url"`  // links: Say's say:[host]:port
}

type osURL struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type osIM struct {
	Label   string `json:"label"`
	Service string `json:"service"`
	User    string `json:"user"`
}

type osBookData struct {
	People      []osPerson `json:"people"`
	FamilyFirst bool       `json:"family_first"` // names shown family name first
	Timing      string     `json:"timing"`       // how long each stage took (the log)
	Error       string     `json:"error"`
}

// SystemBook is an empty book of the system's (while it is read).
func SystemBook() *Book { return &Book{os: true} }

// System reports whether the book is the system's.
func (b *Book) System() bool { return b.os }

// SystemMe is the card the system has as ours ("My Card"; "" for none):
// offered when no card has this Say's address yet. Contacts takes seconds
// for it.
func SystemMe() (string, error) { return readOSMe() }

// LoadOS reads the system's address book.
func LoadOS() (*Book, error) {
	data, err := readOS()
	if err != nil {
		return &Book{os: true}, err
	}
	var d osBookData
	if err := json.Unmarshal(data, &d); err != nil {
		return &Book{os: true}, err
	}
	b := &Book{os: true, Timing: "access " + accessTook.Round(time.Millisecond).String() + ", " + d.Timing}
	for _, op := range d.People {
		p := &Person{UID: op.ID, Given: op.Given, Family: op.Family, Middle: op.Middle, Nick: op.Nick}
		switch {
		case joinName(op.Given, op.Middle, op.Family) == "":
			p.fn = op.Name // a company
		case d.FamilyFirst:
			p.fn = joinName(op.Family, op.Given, op.Middle)
		}
		for _, u := range op.URL {
			if d, ok := deviceOf(prop{name: "URL", value: u.URL}); ok {
				d.Label = abLabel(u.Label)
				p.Devices = addDevice(p.Devices, d)
			}
		}
		for _, im := range op.IM {
			if d, ok := sayIM(im); ok {
				p.Devices = addDevice(p.Devices, d)
			}
		}
		b.add(p)
	}
	b.sort()
	if d.Error != "" {
		return b, errors.New(d.Error)
	}
	return b, nil
}

// sayIM reads a Say address from an instant messaging address.
func sayIM(im osIM) (Device, bool) {
	user := strings.TrimSpace(im.User)
	if !strings.EqualFold(im.Service, "say") && !strings.HasPrefix(strings.ToLower(user), "say:") {
		return Device{}, false
	}
	if strings.HasPrefix(strings.ToLower(user), "say:") && !strings.HasPrefix(strings.ToLower(user), "say://") {
		user = user[len("say:"):]
	}
	host, port, err := ParseAddr(user)
	if err != nil {
		return Device{}, false
	}
	return Device{Label: abLabel(im.Label), Host: host, Port: port}, true
}

// saveOS writes a person's names and Say addresses (links, under their
// labels; Say's instant messaging addresses of before removed) to the
// system's book (a new card when it has no id yet).
func saveOS(p *Person) error {
	op := osPerson{ID: p.UID, Given: p.Given, Family: p.Family, Middle: p.Middle, Nick: p.Nick}
	for _, d := range p.Devices {
		op.URL = append(op.URL, osURL{Label: d.Label, URL: d.uri()})
	}
	in, err := json.Marshal(op)
	if err != nil {
		return err
	}
	out, err := writeOS(in)
	if err != nil {
		return err
	}
	var res struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	p.UID = res.ID
	p.fn = joinName(p.Given, p.Middle, p.Family)
	return nil
}
