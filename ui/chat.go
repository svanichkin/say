package ui

import (
	"fmt"
	"github.com/svanichkin/say/lang"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type ChatMessage struct {
	Text  string
	Local bool
}

var chatState struct {
	sync.RWMutex
	messages []ChatMessage
	input    string
	typedAt  time.Time // last edit; the cursor stays solid right after typing
	// A file being sent and one being received each show their progress
	// in a chat entry that is updated in place (see liveEntry).
	sending   liveEntry
	receiving liveEntry
	onText    func(string)
	onFile    func(string)
}

// liveEntry is a chat entry updated in place: a file transfer's progress,
// replaced by its result when it ends.
type liveEntry struct {
	active bool
	idx    int // 1-based index into chatState.messages; 0: not shown yet
	rate   transferRate
}

// SetChatActions installs the active call's message and file send handlers.
func SetChatActions(onText, onFile func(string)) {
	chatState.Lock()
	chatState.onText, chatState.onFile = onText, onFile
	if onText == nil {
		chatState.input = ""
		chatState.sending = liveEntry{}
		chatState.receiving = liveEntry{}
	}
	chatState.Unlock()
	RequestRedraw()
}

// ChatConnected reports whether an active peer can receive chat messages.
func ChatConnected() bool {
	chatState.RLock()
	defer chatState.RUnlock()
	return chatState.onText != nil
}

// AddChatMessage appends a message to the visible conversation.
func AddChatMessage(text string, local bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	chatState.Lock()
	appendMessageLocked(ChatMessage{Text: text, Local: local})
	chatState.Unlock()
	RequestRedraw()
}

// chatHistoryLimit is how many chat entries are kept.
const chatHistoryLimit = 500

// appendMessageLocked adds an entry, dropping the oldest beyond the limit
// (and keeping the live entries pointing at theirs).
func appendMessageLocked(m ChatMessage) {
	chatState.messages = append(chatState.messages, m)
	if drop := len(chatState.messages) - chatHistoryLimit; drop > 0 {
		chatState.messages = append([]ChatMessage(nil), chatState.messages[drop:]...)
		for _, e := range []*liveEntry{&chatState.sending, &chatState.receiving} {
			e.idx = max(0, e.idx-drop)
		}
	}
}

// setLiveLocked shows text in e's chat entry, adding the entry if needed.
func setLiveLocked(e *liveEntry, local bool, text string) {
	if e.idx > 0 && e.idx <= len(chatState.messages) {
		chatState.messages[e.idx-1].Text = text
		return
	}
	appendMessageLocked(ChatMessage{Text: text, Local: local})
	e.idx = len(chatState.messages)
}

// progressText describes a transfer in progress: how much of how much, the
// percentage and the current speed.
func progressText(verb, name string, done, total int64, rate *transferRate) string {
	pct := int64(0)
	if total > 0 {
		pct = done * 100 / total
	}
	text := fmt.Sprintf("%s %s  %s / %s  %d%%", verb, filepath.Base(name), FormatBytes(done), FormatBytes(total), pct)
	if speed := rate.observe(done, time.Now()); speed > 0 {
		text += "  " + FormatByteRate(speed)
	}
	return text
}

// cursorBlinkPeriod is one on or off phase of the input cursor.
const cursorBlinkPeriod = 530 * time.Millisecond

// blinkEpoch is where the cursor's blink counts from until something is
// typed: from the zero time the span overflows and stays put, and the
// cursor would not blink.
var blinkEpoch = time.Now()

// inputCursorVisible reports the blink phase of the input cursor at now.
func inputCursorVisible(now time.Time) bool {
	chatState.RLock()
	from := chatState.typedAt
	chatState.RUnlock()
	if from.IsZero() {
		from = blinkEpoch
	}
	return (now.Sub(from)/cursorBlinkPeriod)%2 == 0
}

func chatKey(r rune) {
	chatState.Lock()
	chatState.typedAt = time.Now()
	if chatState.onText == nil {
		chatState.Unlock()
		return
	}
	switch r {
	case '\r', '\n':
		line := strings.TrimSpace(chatState.input)
		chatState.input = ""
		if line != "" {
			if info, ok := droppedFile(line); ok {
				if onFile := chatState.onFile; onFile != nil {
					if !startSendingLocked(info) {
						chatState.input = line // keep it to send later
						chatState.Unlock()
						RequestRedraw()
						return
					}
					chatState.Unlock()
					go onFile(info)
					RequestRedraw()
					return
				}
			}
			if onText := chatState.onText; onText != nil {
				text := line
				chatState.Unlock()
				go onText(text)
				AddChatMessage(text, true)
				return
			}
			chatState.messages = append(chatState.messages, ChatMessage{Text: lang.T(lang.NotConnected), Local: false})
		}
	case 0x7f, 0x08:
		if chatState.input != "" {
			_, n := utf8.DecodeLastRuneInString(chatState.input)
			chatState.input = chatState.input[:len(chatState.input)-n]
		}
	default:
		if r >= 0x20 && !unicode.IsControl(r) {
			chatState.input += string(r)
		}
	}
	chatState.Unlock()
	RequestRedraw()
}

func chatPaste(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	chatState.RLock()
	connected := chatState.onText != nil
	chatState.RUnlock()
	if !connected {
		return
	}
	if abs, ok := droppedFile(s); ok {
		chatState.Lock()
		onFile := chatState.onFile
		if onFile == nil || !startSendingLocked(abs) {
			onFile = nil
		}
		chatState.Unlock()
		if onFile != nil {
			go onFile(abs)
			RequestRedraw()
			return
		}
	}
	chatState.Lock()
	chatState.typedAt = time.Now()
	chatState.input += strings.ReplaceAll(s, "\n", " ")
	chatState.Unlock()
	RequestRedraw()
}

// startSendingLocked marks a file as being sent. Only one file is sent at a
// time: otherwise it notes that in the chat and returns false.
func startSendingLocked(path string) bool {
	if chatState.sending.active {
		appendMessageLocked(ChatMessage{Text: lang.T(lang.WaitFile, filepath.Base(path)), Local: true})
		return false
	}
	chatState.sending = liveEntry{active: true}
	return true
}

// SetTransferProgress shows the progress of the file being sent in its chat
// entry on our side.
func SetTransferProgress(name string, sent, total int64) {
	chatState.Lock()
	setLiveLocked(&chatState.sending, true, progressText(lang.T(lang.Sending), name, sent, total, &chatState.sending.rate))
	chatState.Unlock()
	RequestRedraw()
}

// SetTransferStatus shows a status of the file being sent (e.g. waiting for
// the peer's confirmation) in its chat entry.
func SetTransferStatus(text string) {
	chatState.Lock()
	setLiveLocked(&chatState.sending, true, text)
	chatState.Unlock()
	RequestRedraw()
}

// FinishTransfer replaces the sent file's entry with the result; another
// file can be sent after it.
func FinishTransfer(text string) {
	chatState.Lock()
	setLiveLocked(&chatState.sending, true, text)
	chatState.sending = liveEntry{}
	chatState.Unlock()
	RequestRedraw()
}

// SetReceiveProgress shows an incoming file's progress in its chat entry on
// the peer's side; FinishReceive replaces it with the result.
func SetReceiveProgress(name string, received, total int64) {
	chatState.Lock()
	chatState.receiving.active = true
	setLiveLocked(&chatState.receiving, false, progressText(lang.T(lang.Receiving), name, received, total, &chatState.receiving.rate))
	chatState.Unlock()
	RequestRedraw()
}

// FinishReceive replaces the incoming file's entry with the result, or adds
// it as a new entry if there is none.
func FinishReceive(text string) {
	chatState.Lock()
	setLiveLocked(&chatState.receiving, false, text)
	chatState.receiving = liveEntry{}
	chatState.Unlock()
	RequestRedraw()
}

// TransferSummary describes a finished transfer for its chat entry:
// size, average speed and time.
func TransferSummary(size int64, elapsed time.Duration) string {
	text := FormatBytes(size)
	if elapsed > 0 && size > 0 {
		text += ", " + FormatByteRate(float64(size)/elapsed.Seconds()) + ", " + elapsed.Round(100*time.Millisecond).String()
	}
	return text
}

// droppedFile resolves text that a terminal inserts when a file is dragged
// onto it, or that is typed or pasted as a path, to an existing regular file.
// Terminals differ: macOS Terminal and iTerm2 escape spaces and special
// characters with backslashes ("My\ File.txt") and append a space, others
// quote the path or paste a file:// URI.
func droppedFile(text string) (string, bool) {
	path := strings.TrimSpace(text)
	if len(path) >= 2 && (path[0] == '"' || path[0] == '\'') && path[len(path)-1] == path[0] {
		path = path[1 : len(path)-1]
	} else if strings.Contains(path, "\\") {
		var b strings.Builder
		escaped := false
		for _, r := range path {
			if r == '\\' && !escaped {
				escaped = true
				continue
			}
			escaped = false
			b.WriteRune(r)
		}
		path = b.String()
	}
	if strings.HasPrefix(path, "file://") {
		if parsed, err := url.Parse(path); err == nil {
			if decoded, err := url.PathUnescape(parsed.Path); err == nil {
				path = decoded
			}
		}
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	if path == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return abs, true
}
