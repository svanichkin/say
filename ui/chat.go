package ui

import (
	"fmt"
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
	transfer string
	onText   func(string)
	onFile   func(string)
}

// SetChatActions installs the active call's message and file send handlers.
func SetChatActions(onText, onFile func(string)) {
	chatState.Lock()
	chatState.onText, chatState.onFile = onText, onFile
	if onText == nil {
		chatState.input = ""
		chatState.transfer = ""
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
	chatState.messages = append(chatState.messages, ChatMessage{Text: text, Local: local})
	if len(chatState.messages) > 500 {
		chatState.messages = append([]ChatMessage(nil), chatState.messages[len(chatState.messages)-500:]...)
	}
	chatState.Unlock()
	RequestRedraw()
}

// cursorBlinkPeriod is one on or off phase of the input cursor.
const cursorBlinkPeriod = 530 * time.Millisecond

// inputCursorVisible reports the blink phase of the input cursor at now.
func inputCursorVisible(now time.Time) bool {
	chatState.RLock()
	since := now.Sub(chatState.typedAt)
	chatState.RUnlock()
	return (since/cursorBlinkPeriod)%2 == 0
}

func chatKey(r rune) {
	chatState.Lock()
	chatState.typedAt = time.Now()
	if chatState.onText == nil {
		chatState.Unlock()
		return
	}
	if chatState.transfer != "" {
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
					chatState.transfer = filepath.Base(info)
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
			chatState.messages = append(chatState.messages, ChatMessage{Text: "Not connected", Local: false})
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
	busy := chatState.transfer != "" || chatState.onText == nil
	chatState.RUnlock()
	if busy {
		return
	}
	if abs, ok := droppedFile(s); ok {
		chatState.Lock()
		onFile := chatState.onFile
		if onFile != nil && chatState.transfer == "" {
			chatState.transfer = filepath.Base(abs)
		} else {
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

// SetTransferProgress replaces the input line while a file is being sent.
func SetTransferProgress(name string, sent, total int64, done bool) {
	chatState.Lock()
	if done {
		chatState.transfer = ""
	} else {
		pct := int64(0)
		if total > 0 {
			pct = sent * 100 / total
		}
		chatState.transfer = fmt.Sprintf("Sending %s  %d%%", filepath.Base(name), pct)
	}
	chatState.Unlock()
	RequestRedraw()
}

// SetTransferStatus shows a status text in the input line while a transfer is active.
func SetTransferStatus(text string) {
	chatState.Lock()
	chatState.transfer = text
	chatState.Unlock()
	RequestRedraw()
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
