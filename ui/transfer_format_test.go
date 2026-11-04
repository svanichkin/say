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

func TestTransferProgressLine(t *testing.T) {
	SetTransferProgress("/tmp/report.pdf", 0, 10<<20, false)
	SetTransferProgress("/tmp/report.pdf", 3<<20, 10<<20, false)
	chatState.RLock()
	line := chatState.transfer
	chatState.RUnlock()
	SetTransferProgress("/tmp/report.pdf", 10<<20, 10<<20, true)
	if !strings.HasPrefix(line, "Sending report.pdf  3.0 MB / 10 MB  30%") {
		t.Fatalf("progress line %q", line)
	}
}
