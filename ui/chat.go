package ui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func chatKey(r rune) {
	chatState.Lock()
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
			if info, err := filepath.Abs(strings.Trim(line, "\"'")); err == nil {
				if st, statErr := os.Stat(info); statErr == nil && st.Mode().IsRegular() {
					if onFile := chatState.onFile; onFile != nil {
						if chatState.transfer != "" {
							chatState.Unlock()
							return
						}
						chatState.transfer = filepath.Base(info)
						chatState.Unlock()
						go onFile(info)
						RequestRedraw()
						return
					}
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
	// Terminal file managers commonly paste file:// URIs or quoted absolute paths.
	path := strings.Trim(s, "\"'\r\n")
	if strings.HasPrefix(path, "file://") {
		if parsed, err := url.Parse(path); err == nil {
			if decoded, err := url.PathUnescape(parsed.Path); err == nil {
				path = decoded
			}
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		if st, statErr := os.Stat(abs); statErr == nil && st.Mode().IsRegular() {
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
	}
	chatState.Lock()
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
