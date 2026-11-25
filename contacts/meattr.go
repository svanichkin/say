package contacts

import "errors"

// Our own card in a folder is the one whose file is marked "me": an
// extended attribute any program can read and set (user.me on Linux,
// which takes only user.* from users; an alternate data stream on
// Windows), its value the card's UID (a file may hold several cards; none:
// the file's first). Plan 9 and file systems without attributes take no
// mark: there a folder has no card of ours.
const meAttr = "me"

// errNoAttr: no extended attributes here.
var errNoAttr = errors.New("no extended attributes")
