// Package keys names the keys that are not characters, as the terminal
// reader and the windows pass them to the interface: runes of the private
// use area (as macOS's NSUpArrowFunctionKey), never typed as text.
package keys

const (
	Up    rune = 0xF700
	Down  rune = 0xF701
	Left  rune = 0xF702
	Right rune = 0xF703
	Esc   rune = 0x1b
	Tab   rune = '\t'
)

// Special reports whether r is one of these keys, not text.
func Special(r rune) bool { return r >= Up && r <= Right || r == Esc || r == Tab }
