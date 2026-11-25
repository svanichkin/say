package contacts

import (
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T, cards map[string]string, self string) *Store {
	t.Helper()
	dir := t.TempDir()
	for name, c := range cards {
		write(t, dir, name, c)
	}
	s := NewStore(filepath.Join(t.TempDir(), "c-contacts.json"), dir, "laptop", 7777, nil)
	s.SetSelf(self)
	return s
}

// Our card as sent: the names, the nickname, the devices with their labels;
// nothing else of the card (the photo, the phone).
func TestMyCardSent(t *testing.T) {
	s := testStore(t, map[string]string{"me.vcf": "BEGIN:VCARD\nVERSION:3.0\nUID:ME\nN:Ivanova;Alice;;;\nFN:Alice Ivanova\nNICKNAME:al\nTEL:+7 900\nPHOTO;ENCODING=b:AAAA\n" +
		"item1.URL:say:[200::1]\nitem1.X-ABLabel:laptop\nitem2.URL:say:[200::2]:7001\nitem2.X-ABLabel:phone\nEND:VCARD\n"}, "200::1")
	if s.Snapshot().Me == nil {
		t.Skip("no extended attributes: no card of ours in a folder")
	}
	data := string(s.MyCard())
	for _, want := range []string{"FN:Alice Ivanova", "NICKNAME:al", "say:[200::1]", "X-ABLabel:phone", "say:[200::2]:7001"} {
		if !strings.Contains(data, want) {
			t.Errorf("no %q in\n%s", want, data)
		}
	}
	for _, not := range []string{"TEL", "PHOTO", "UID"} {
		if strings.Contains(data, not) {
			t.Errorf("%s sent:\n%s", not, data)
		}
	}
}

const bobCard = "BEGIN:VCARD\nVERSION:3.0\nN:Smith;Bob;;;\nFN:Bob Smith\nNICKNAME:bobby\n" +
	"item1.URL:say:[201::1]\nitem1.X-ABLabel:home\nitem2.URL:say:[201::2]\nitem2.X-ABLabel:phone\nEND:VCARD\n"

// A card from an address in the book: that person's; what it adds (a second
// device, the nickname) is written, nothing written over.
func TestReceivedKnown(t *testing.T) {
	s := testStore(t, map[string]string{"bob.vcf": "BEGIN:VCARD\nVERSION:3.0\nUID:B\nFN:Бобик\nitem1.URL:say:[201::1]\nitem1.X-ABLabel:дом\nEND:VCARD\n"}, "200::1")
	name, label := s.Received("201::1", []byte(bobCard))
	if name != "Бобик" || label != "home" {
		t.Fatalf("shown as %q, %q", name, label)
	}
	p := s.Snapshot().ByUID("B")
	if p.Name() != "Бобик" || p.Nick != "bobby" || len(p.Devices) != 2 || p.Devices[0].Label != "дом" || p.Devices[1].Host != "201::2" {
		t.Fatalf("Bob after: %q %q %+v", p.Name(), p.Nick, p.Devices)
	}
	if len(s.Snapshot().Pending) != 0 {
		t.Fatal("a known address's card pending")
	}
}

// A card from an address not in the book waits, pending; confirmed it is a
// new card (or added to the one of that name); deleted it is gone.
func TestReceivedPending(t *testing.T) {
	s := testStore(t, map[string]string{"bob2.vcf": "BEGIN:VCARD\nVERSION:3.0\nUID:B2\nN:Smith;Bob;;;\nFN:Bob Smith\nitem1.URL:say:[209::9]\nEND:VCARD\n"}, "200::1")
	s.Received("201::1", []byte(bobCard))
	sn := s.Snapshot()
	if len(sn.Pending) != 1 || sn.Pending[0].From != "201::1" || len(sn.People) != 1 {
		t.Fatalf("pending %d, people %d", len(sn.Pending), len(sn.People))
	}
	same := sn.SameName(sn.Pending[0].Person)
	if len(same) != 1 || same[0].UID != "B2" {
		t.Fatalf("the same name: %+v", same)
	}
	if err := s.Confirm(sn.Pending[0], "B2"); err != nil {
		t.Fatal(err)
	}
	sn = s.Snapshot()
	if p := sn.ByUID("B2"); len(sn.Pending) != 0 || len(p.Devices) != 3 || p.Nick != "bobby" {
		t.Fatalf("added to Bob: %+v, pending %d", p, len(sn.Pending))
	}
	s.Received("203::3", []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Eve\nitem1.URL:say:[203::3]\nEND:VCARD\n"))
	s.Reject(s.Snapshot().Pending[0])
	if len(s.Snapshot().Pending) != 0 {
		t.Fatal("deleted, still pending")
	}
	s.Received("204::4", []byte("BEGIN:VCARD\nVERSION:3.0\nN:;Carol;;;\nFN:Carol\nitem1.URL:say:[204::4]\nEND:VCARD\n"))
	if err := s.Confirm(s.Snapshot().Pending[0], ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Snapshot().byHost("204::4"); p == nil || p.Name() != "Carol" {
		t.Fatalf("Carol confirmed: %+v", p)
	}
}

// A received card without the device it came from is not kept; names come
// plain (no terminal sequences).
func TestReceivedChecked(t *testing.T) {
	s := testStore(t, nil, "200::1")
	s.Received("205::5", []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Mallory\nitem1.URL:say:[201::1]\nEND:VCARD\n"))
	if len(s.Snapshot().Pending) != 0 {
		t.Fatal("a card without its own device kept")
	}
	name, _ := s.Received("206::6", []byte("BEGIN:VCARD\nVERSION:3.0\nFN:Ev\x1b[2Je\nitem1.URL:say:[206::6]\nEND:VCARD\n"))
	if name != "Ev[2Je" || len(s.Snapshot().Pending) != 1 {
		t.Fatalf("shown as %q, pending %d", name, len(s.Snapshot().Pending))
	}
}
