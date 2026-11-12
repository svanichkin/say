// Package lang holds what Say says to the user, in the user's language: the
// system's (the first of its preferred languages that Say speaks) or the one
// -lang names. English is the default and fills any gap.
package lang

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	locale "github.com/jeandeaual/go-locale"
)

// Key names a text.
type Key int

const (
	BootServer Key = iota
	BootClient
	Failure
	JackIn
	YggMesh
	RawLink
	NodeOnline
	HandOver
	Listening
	Dialing
	Punching
	Redial
	Attempt
	Uplink
	Handshake
	SignalLost
	Rerouting
	LineBusy
	BusyRetry
	NoCarrier
	Retry
	Standby
	AwaitSignal
	LinkActive
	TooSmall
	Mismatch
	PeerRuns
	YouRun
	PatchBoth
	OlderVersion
	ThisVersion
	NoSignal
	Copy
	Copied
	LegacyPeer
	UnknownCodec
	Codecs
	MsgFailed
	NotConnected
	WaitFile
	Sending
	Receiving
	Sent
	Unconfirmed
	Received
	RxFailed
	RxInterrupted
	TxFailed
	NotRegular
	PeerSaid
	AwaitConfirm
	AskMode
	Stopped

	numKeys
)

// The status screens take a heading (in capitals, where the script has
// them) on the first line and the details below it.
var en = map[Key]string{
	BootServer:    "BOOTING\nServer node",
	BootClient:    "BOOTING\nClient node",
	Failure:       "SYSTEM FAILURE",
	JackIn:        "JACKING IN",
	YggMesh:       "Yggdrasil mesh",
	RawLink:       "Raw TCP/UDP link",
	NodeOnline:    "NODE ONLINE",
	HandOver:      "Hand this line to whoever calls in",
	Listening:     "Listening on port %d",
	Dialing:       "DIALING",
	Punching:      "Punching through the mesh",
	Redial:        "REDIAL",
	Attempt:       "Attempt #%d",
	Uplink:        "UPLINK ESTABLISHED",
	Handshake:     "Handshake complete",
	SignalLost:    "SIGNAL LOST",
	Rerouting:     "Rerouting",
	LineBusy:      "LINE BUSY",
	BusyRetry:     "On another call · retry in %d s",
	NoCarrier:     "NO CARRIER",
	Retry:         "Retry in %d s",
	Standby:       "STANDBY",
	AwaitSignal:   "Awaiting signal",
	LinkActive:    "LINK ACTIVE",
	TooSmall:      "Viewport too small to render the feed",
	Mismatch:      "PROTOCOL MISMATCH",
	PeerRuns:      "Peer runs Say %s (protocol %d)",
	YouRun:        "You run Say %s (protocol %d)",
	PatchBoth:     "Patch both sides to the same version",
	OlderVersion:  "an older version",
	ThisVersion:   "this version",
	NoSignal:      "NO SIGNAL",
	Copy:          "Copy",
	Copied:        "Copied",
	LegacyPeer:    "The peer runs an older Say version. Voice works only if both sides use the same -audio-codec; update both sides to the same version.",
	UnknownCodec:  "The peer sends voice as %s, which this Say version cannot decode. Update both sides to the same version.",
	Codecs:        "Voice: you send %s, the peer sends %s.",
	MsgFailed:     "Message send failed: %s",
	NotConnected:  "Not connected",
	WaitFile:      "Wait until the current file is sent: %s",
	Sending:       "Sending",
	Receiving:     "Receiving",
	Sent:          "Sent %s (%s)",
	Unconfirmed:   "%s, but the peer did not confirm saving it",
	Received:      "Received %s (%s), saved to Downloads",
	RxFailed:      "File receive failed: %s: %s",
	RxInterrupted: "File receive interrupted: %s",
	TxFailed:      "File send failed: %s: %s",
	NotRegular:    "only regular files can be sent",
	PeerSaid:      "peer: %s",
	AwaitConfirm:  "Awaiting confirmation: %s",
	AskMode:       "GUI or TUI? [G/t] ",
	Stopped:       "Say stopped: %v\n\nDetails, if it crashed: %s",
}

func init() { tables["en"] = en }

var current atomic.Pointer[string]

// Code is the language in use.
func Code() string {
	if c := current.Load(); c != nil {
		return *c
	}
	return "en"
}

// T is the text for k in the language in use (English where it has none),
// with args put in as fmt does.
func T(k Key, args ...any) string {
	s := tables[Code()][k]
	if s == "" {
		s = en[k]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// En is the text for k in English, as T does it: for the log.
func En(k Key, args ...any) string {
	if len(args) == 0 {
		return en[k]
	}
	return fmt.Sprintf(en[k], args...)
}

// Supported lists the language codes Say speaks.
func Supported() []string {
	out := make([]string, 0, len(tables))
	for c := range tables {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Match finds the language Say speaks for a tag (ru, pt-BR, zh_TW.UTF-8 and
// the like); ok is false when it speaks none of it.
func Match(tag string) (code string, ok bool) {
	t := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(t, ".@"); i >= 0 {
		t = t[:i]
	}
	t = strings.ReplaceAll(t, "_", "-")
	if t == "" {
		return "", false
	}
	base, region, _ := strings.Cut(t, "-")
	switch base {
	case "zh":
		if strings.Contains(t, "hant") || slices.Contains([]string{"tw", "hk", "mo"}, region) {
			return "zh-Hant", true
		}
		return "zh", true
	case "no", "nn":
		base = "nb"
	}
	if _, ok := tables[base]; ok {
		return base, true
	}
	return "", false
}

// Set picks the language for a tag; false when Say does not speak it.
func Set(tag string) bool {
	code, ok := Match(tag)
	if ok {
		current.Store(&code)
	}
	return ok
}

// Init picks the language: the one -lang names among args, else the first
// of the system's preferred languages that Say speaks, else English.
func Init(args []string) {
	if tag, given := flagValue(args); given && Set(tag) {
		return
	}
	tags, _ := locale.GetLocales()
	if len(tags) == 0 {
		if t, err := locale.GetLocale(); err == nil {
			tags = []string{t}
		}
	}
	for _, t := range tags {
		if Set(t) {
			return
		}
	}
}

// flagValue finds -lang (or --lang) among the arguments: -lang=xx or
// -lang xx.
func flagValue(args []string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "lang" {
			continue
		}
		if hasValue {
			return value, true
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
