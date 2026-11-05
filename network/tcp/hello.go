package tcp

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/svanichkin/say/ui"
)

// Call protocol versions. ProtocolVersion covers the handshake, the control
// frames and the media formats: raise it with any change a peer of the
// previous version cannot handle. MinProtocolVersion is the oldest peer this
// build still talks to; raise it when support for older peers is dropped.
//
//	1  v1.0.0: no version in the handshake (a peer without one is taken as 1)
//	2  the handshake carries versions and the codec each side sends
const (
	ProtocolVersion    = 2
	MinProtocolVersion = 1
)

// incompatibleReply answers a HELLO from a peer this build cannot talk to.
const incompatibleReply = "INCOMPATIBLE"

// PeerInfo is what a peer announced in the handshake.
type PeerInfo struct {
	Name string
	// Proto is the peer's protocol version and Min the oldest it talks to.
	Proto, Min int
	// App is the peer's Say version ("" for peers before protocol 2).
	App string
	// AudioCodec is the codec the peer encodes its voice with ("" if not
	// announced: such a peer expects ours).
	AudioCodec string
	// Legacy is set for a peer that announced nothing (before protocol 2).
	Legacy bool
}

var localInfo struct {
	sync.RWMutex
	app, codec string
}

// SetLocalInfo sets the Say version and the voice codec this side announces.
func SetLocalInfo(app, audioCodec string) {
	localInfo.Lock()
	localInfo.app, localInfo.codec = helloToken(app), helloToken(audioCodec)
	localInfo.Unlock()
}

// LocalApp returns the announced Say version.
func LocalApp() string {
	localInfo.RLock()
	defer localInfo.RUnlock()
	return localInfo.app
}

// helloToken keeps a value to one word: the fields are space separated.
func helloToken(s string) string {
	return strings.Join(strings.Fields(s), "_")
}

// helloFields is appended to HELLO and OK after a tab. Peers of protocol 1
// read only the word after HELLO/OK, so the extra fields do not disturb them.
func helloFields() string {
	localInfo.RLock()
	defer localInfo.RUnlock()
	f := fmt.Sprintf("proto=%d min=%d", ProtocolVersion, MinProtocolVersion)
	if localInfo.app != "" {
		f += " app=" + localInfo.app
	}
	if localInfo.codec != "" {
		f += " codec=" + localInfo.codec
	}
	return f
}

// helloLine builds a handshake line: "<verb> <name>\t<fields>\n".
func helloLine(verb, name string) string {
	return verb + " " + strings.ReplaceAll(name, "\t", " ") + "\t" + helloFields() + "\n"
}

// parseHello splits a handshake line after its verb into the name and the
// announced fields. A line without fields comes from a protocol 1 peer.
func parseHello(rest string) PeerInfo {
	rest = strings.TrimRight(rest, "\r\n")
	name, fields, tagged := strings.Cut(rest, "\t")
	info := PeerInfo{Name: strings.TrimSpace(name), Proto: 1, Min: 1, Legacy: !tagged}
	if !tagged {
		return info
	}
	for _, f := range strings.Fields(fields) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "proto":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				info.Proto = n
			}
		case "min":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				info.Min = n
			}
		case "app":
			info.App = v
		case "codec":
			info.AudioCodec = v
		}
	}
	return info
}

// compatible reports whether this build and the peer can talk.
func compatible(peer PeerInfo) bool {
	return peer.Proto >= MinProtocolVersion && ProtocolVersion >= peer.Min
}

// IncompatibleError reports a peer whose protocol version does not fit.
type IncompatibleError struct{ Peer PeerInfo }

func (e *IncompatibleError) Error() string {
	return IncompatibleText(e.Peer)
}

// IncompatibleText explains an incompatible peer to the user.
func IncompatibleText(peer PeerInfo) string {
	app := peer.App
	if app == "" {
		app = "an older version"
	}
	ours := LocalApp()
	if ours == "" {
		ours = "this version"
	}
	return fmt.Sprintf("Incompatible versions: the peer runs Say %s (protocol %d), you run Say %s (protocol %d). Update both sides to the same version.",
		app, peer.Proto, ours, ProtocolVersion)
}

// notePeer tells the user in the chat about a peer from before protocol
// versions: its voice is understood only with the same codec.
func notePeer(peer PeerInfo) {
	if peer.Legacy {
		log.Printf("[p2p] the peer runs an older Say without version info (protocol 1)")
		ui.AddChatMessage("The peer runs an older Say version. Voice works only if both sides use the same -audio-codec; update both sides to the same version.", false)
		return
	}
	log.Printf("[p2p] peer runs Say %s (protocol %d), sends voice as %s", peer.App, peer.Proto, peer.AudioCodec)
}
