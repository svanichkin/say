package tcp

import (
	"bufio"
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// A connection must start with the terminal size as it is now: the start-up
// size is stale once the window changed, and that change may already have
// been taken from the channel by an earlier call.
func TestTermSizeSenderSendsCurrentSize(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	sizes := make(chan TermSize, 4)
	var mu sync.Mutex
	current := TermSize{Cols: 78, Rows: 39}
	stop := make(chan func(), 1)
	go func() {
		stop <- startTermSizeSender(context.Background(), a, sizes, &TermSize{Cols: 30, Rows: 15},
			func() (TermSize, bool) { mu.Lock(); defer mu.Unlock(); return current, true })
	}()
	r := bufio.NewReader(b)
	read := func() (int, int) {
		_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
		kind, payload, err := readFrame(r)
		if err != nil || kind != frameTermSize {
			t.Fatalf("frame %d: %v", kind, err)
		}
		cols, rows, ok := decodeTermSizeFrame(payload)
		if !ok {
			t.Fatal("bad term size frame")
		}
		return cols, rows
	}
	if cols, rows := read(); cols != 78 || rows != 39 {
		t.Fatalf("first size %dx%d, want the current 78x39", cols, rows)
	}
	defer (<-stop)()
	// A queued update is only a signal: it never sends an older size.
	sizes <- TermSize{Cols: 30, Rows: 15}
	mu.Lock()
	current = TermSize{Cols: 100, Rows: 50}
	mu.Unlock()
	sizes <- TermSize{Cols: 100, Rows: 50}
	if cols, rows := read(); cols != 100 || rows != 50 {
		t.Fatalf("update %dx%d, want 100x50", cols, rows)
	}
}
