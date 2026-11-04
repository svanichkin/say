package ui

import (
	"strings"
	"testing"
	"time"
)

func TestFormatBytesPicksTheUnit(t *testing.T) {
	for n, want := range map[int64]string{
		0:                       "0 B",
		512:                     "512 B",
		1536:                    "1.5 kB",
		500 * 1024:              "500 kB",
		3*1024*1024 + 400*1024:  "3.4 MB",
		750 * 1024 * 1024:       "750 MB",
		5 * 1024 * 1024 * 1024:  "5.0 GB",
		42 * 1024 * 1024 * 1024: "42 GB",
	} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
	if got := FormatByteRate(2.5 * 1024 * 1024); got != "2.5 MBps" {
		t.Errorf("FormatByteRate = %q, want 2.5 MBps", got)
	}
}

func TestTransferRateFollowsTheLastSeconds(t *testing.T) {
	var r transferRate
	start := time.Now()
	var speed float64
	// 1 MB/s for 3 s, then 4 MB/s for 3 s, a chunk every 50 ms.
	sent := int64(0)
	for i := 1; i <= 120; i++ {
		rate := int64(1 << 20)
		if i > 60 {
			rate = 4 << 20
		}
		sent += rate / 20
		speed = r.observe(sent, start.Add(time.Duration(i)*50*time.Millisecond))
		if i == 60 && (speed < 0.9*(1<<20) || speed > 1.1*(1<<20)) {
			t.Fatalf("after 3 s at 1 MB/s: %.0f B/s", speed)
		}
	}
	if speed < 3.6*(1<<20) || speed > 4.4*(1<<20) {
		t.Fatalf("after 3 s at 4 MB/s: %.0f B/s", speed)
	}
	// A new transfer starts over.
	if got := r.observe(0, start.Add(10*time.Second)); got != 0 {
		t.Fatalf("new transfer: %.0f B/s, want 0", got)
	}
}

// lastMessages returns the chat entries' texts and sides.
func lastMessages() []ChatMessage {
	chatState.RLock()
	defer chatState.RUnlock()
	return append([]ChatMessage(nil), chatState.messages...)
}

func TestSentFileShowsProgressInItsChatEntry(t *testing.T) {
	SetChatActions(func(string) {}, func(string) {})
	defer SetChatActions(nil, nil)
	chatState.Lock()
	chatState.messages = nil
	if !startSendingLocked("/tmp/report.pdf") {
		t.Fatal("first file refused")
	}
	chatState.Unlock()

	SetTransferProgress("/tmp/report.pdf", 0, 10<<20)
	SetTransferProgress("/tmp/report.pdf", 3<<20, 10<<20)
	m := lastMessages()
	if len(m) != 1 || !m[0].Local || !strings.HasPrefix(m[0].Text, "Sending report.pdf  3.0 MB / 10 MB  30%") {
		t.Fatalf("entries %+v", m)
	}
	// Typing and text messages are not blocked while a file is sent.
	chatKey('o')
	chatKey('k')
	chatState.RLock()
	input := chatState.input
	chatState.RUnlock()
	if input != "ok" {
		t.Fatalf("input %q while sending", input)
	}
	// A second file waits.
	chatState.Lock()
	second := startSendingLocked("/tmp/other.bin")
	chatState.Unlock()
	if second {
		t.Fatal("a second file started while one is being sent")
	}
	SetTransferStatus("Waiting for report.pdf confirmation…")
	FinishTransfer("Sent report.pdf (" + TransferSummary(10<<20, 4200*time.Millisecond) + ")")
	m = lastMessages()
	if m[0].Text != "Sent report.pdf (10 MB, 2.4 MBps, 4.2s)" || len(m) != 2 {
		t.Fatalf("entries after the send: %+v", m)
	}
	chatState.Lock()
	again := startSendingLocked("/tmp/other.bin")
	chatState.Unlock()
	if !again {
		t.Fatal("next file refused after the first one finished")
	}
	FinishTransfer("done")
}

func TestReceivedFileShowsProgressInItsChatEntry(t *testing.T) {
	SetChatActions(func(string) {}, func(string) {})
	defer SetChatActions(nil, nil)
	chatState.Lock()
	chatState.messages = nil
	chatState.Unlock()

	SetReceiveProgress("photo.jpg", 0, 4<<20)
	AddChatMessage("hello", false) // the chat goes on meanwhile
	SetReceiveProgress("photo.jpg", 1<<20, 4<<20)
	m := lastMessages()
	if len(m) != 2 || m[0].Local || !strings.HasPrefix(m[0].Text, "Receiving photo.jpg  1.0 MB / 4.0 MB  25%") {
		t.Fatalf("entries %+v", m)
	}
	if out := renderChatScreen(120, 40); !strings.Contains(out, "Receiving photo.jpg") {
		t.Fatal("receive progress not on screen")
	}
	FinishReceive("Received photo.jpg (4.0 MB, 2.4 MBps, 1.7s), saved to Downloads")
	m = lastMessages()
	if len(m) != 2 || m[0].Text != "Received photo.jpg (4.0 MB, 2.4 MBps, 1.7s), saved to Downloads" {
		t.Fatalf("entries after the receive: %+v", m)
	}
}

func TestLiveEntryFollowsHistoryTrim(t *testing.T) {
	SetChatActions(func(string) {}, func(string) {})
	defer SetChatActions(nil, nil)
	chatState.Lock()
	chatState.messages = nil
	chatState.Unlock()
	SetReceiveProgress("big.iso", 0, 1<<30)
	for i := 0; i < chatHistoryLimit+20; i++ {
		AddChatMessage("x", true)
	}
	SetReceiveProgress("big.iso", 1<<29, 1<<30)
	m := lastMessages()
	found := 0
	for _, e := range m {
		if strings.HasPrefix(e.Text, "Receiving big.iso") {
			found++
		}
	}
	if found != 1 || len(m) > chatHistoryLimit {
		t.Fatalf("%d progress entries in %d messages", found, len(m))
	}
	FinishReceive("done")
}
