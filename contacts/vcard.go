// Package contacts is Say's address book: a folder of vCard files (a vdir,
// as vdirsyncer keeps one in step with a CardDAV server), each card a person
// with the Say addresses of their devices.
//
// Cards are read in versions 2.1, 3.0 and 4.0 and written in 3.0, as macOS
// Contacts writes them. A card Say changes keeps every line it does not
// know (photos, phones, notes): only its names and its Say addresses are
// rewritten.
package contacts

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// prop is one content line of a card: group.NAME;params:value.
type prop struct {
	group  string // "item1" in item1.IMPP; "" for none
	name   string // upper case
	spelt  string // the name as the file has it ("" : name)
	params string // what stands between the name and the colon, ";" first, as read
	value  string
}

// card is the lines between BEGIN:VCARD and END:VCARD.
type card struct {
	props []prop
}

// parseCards reads every card in data.
func parseCards(data []byte) []*card {
	var cards []*card
	var cur *card
	for _, line := range unfold(data) {
		p, ok := parseLine(line)
		if !ok {
			continue
		}
		switch {
		case p.name == "BEGIN" && strings.EqualFold(p.value, "VCARD"):
			cur = &card{}
		case p.name == "END" && strings.EqualFold(p.value, "VCARD"):
			if cur != nil {
				cards = append(cards, cur)
			}
			cur = nil
		case cur != nil:
			cur.props = append(cur.props, p)
		}
	}
	return cards
}

// unfold joins folded lines (a line that begins with a space or a tab goes
// on the one before) and quoted-printable soft breaks (vCard 2.1: a line
// ending in "=").
//
// It is one pass: a photo is thousands of folded lines, and joining them
// into a string again for each one took minutes for a folder of cards.
func unfold(data []byte) []string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // a BOM
	var out []string
	var buf []byte
	open, qp := false, false
	end := func() {
		if open {
			out = append(out, string(buf))
		}
	}
	for len(data) > 0 {
		l := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			l, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		l = bytes.TrimRight(l, "\r")
		switch {
		case open && len(l) > 0 && (l[0] == ' ' || l[0] == '\t'):
			buf = append(buf, l[1:]...)
		case open && qp && len(buf) > 0 && buf[len(buf)-1] == '=':
			// A quoted-printable value goes on past a line ending in "=".
			buf = append(buf[:len(buf)-1], l...)
		default:
			end()
			buf = append(buf[:0], l...)
			open = true
			head := l
			if k := bytes.IndexByte(l, ':'); k >= 0 {
				head = l[:k]
			}
			qp = bytes.Contains(bytes.ToUpper(head), []byte("QUOTED-PRINTABLE"))
		}
	}
	end()
	return out
}

// parseLine splits a content line into its parts.
func parseLine(line string) (prop, bool) {
	// The value begins after the first colon outside quotes.
	colon, quoted := -1, false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				colon = i
			}
		}
		if colon >= 0 {
			break
		}
	}
	if colon <= 0 {
		return prop{}, false
	}
	head, value := line[:colon], line[colon+1:]
	name, params := head, ""
	if i := strings.IndexByte(head, ';'); i >= 0 {
		name, params = head[:i], head[i:]
	}
	group := ""
	if i := strings.IndexByte(name, '.'); i >= 0 {
		group, name = name[:i], name[i+1:]
	}
	name = strings.TrimSpace(name)
	return prop{group: group, name: strings.ToUpper(name), spelt: name, params: params, value: value}, true
}

// param is the value of a parameter (case-insensitive name; "" if absent).
// TYPE=a,b and TYPE=a;TYPE=b both come out as "a,b".
func (p prop) param(name string) string {
	var vals []string
	for _, kv := range strings.Split(p.params, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			// vCard 2.1: a bare TYPE value (TEL;HOME:...).
			if name == "TYPE" && kv != "" && !strings.Contains(kv, "=") {
				vals = append(vals, kv)
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), name) {
			vals = append(vals, strings.Trim(v, `"`))
		}
	}
	return strings.Join(vals, ",")
}

// get is the value of the first property called name ("" if none).
func (c *card) get(name string) string {
	for _, p := range c.props {
		if p.name == name {
			return unescape(p.value)
		}
	}
	return ""
}

// set replaces the first property called name (or adds one); value "" removes
// them all.
func (c *card) set(name, value string) {
	if value == "" {
		c.drop(func(p prop) bool { return p.name == name })
		return
	}
	for i, p := range c.props {
		if p.name == name {
			c.props[i].value = value
			return
		}
	}
	c.props = append(c.props, prop{name: name, value: value})
}

// drop removes the properties for which f says so.
func (c *card) drop(f func(prop) bool) {
	kept := c.props[:0]
	for _, p := range c.props {
		if !f(p) {
			kept = append(kept, p)
		}
	}
	c.props = kept
}

// write appends the card to b, its lines folded at 75 octets.
func (c *card) write(b *strings.Builder) {
	writeLine(b, "BEGIN:VCARD")
	version := false
	for _, p := range c.props {
		if p.name == "VERSION" {
			version = true
		}
	}
	if !version {
		writeLine(b, "VERSION:3.0")
	}
	for _, p := range c.props {
		head := p.name
		if p.spelt != "" {
			head = p.spelt
		}
		if p.group != "" {
			head = p.group + "." + head
		}
		writeLine(b, head+p.params+":"+p.value)
	}
	writeLine(b, "END:VCARD")
}

// writeLine writes a content line folded at 75 octets, never inside a
// character.
func writeLine(b *strings.Builder, line string) {
	const width = 75
	first := true
	for len(line) > 0 {
		limit := width
		if !first {
			limit = width - 1 // the space that begins the continuation
			b.WriteByte(' ')
		}
		n := min(limit, len(line))
		for n < len(line) && n > 0 && !utf8.RuneStart(line[n]) {
			n--
		}
		b.WriteString(line[:n])
		b.WriteString("\r\n")
		line = line[n:]
		first = false
	}
}

// unescape undoes a text value's escapes (\, \; \n \\).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// escape escapes a text value.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\n", `\n`)
	return r.Replace(s)
}

// splitValue splits a structured value (N: family;given;middle;...) at
// unescaped semicolons, each part unescaped.
func splitValue(s string) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
		case s[i] == ';':
			parts = append(parts, unescape(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(parts, unescape(cur.String()))
}

// splitList splits a list value (NICKNAME: a,b) at unescaped commas, each
// part unescaped.
func splitList(s string) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
		case s[i] == ',':
			parts = append(parts, unescape(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, unescape(cur.String()))
	}
	return parts
}
