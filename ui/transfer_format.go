package ui

import (
	"fmt"
	"time"
)

// FormatBytes renders a size with a unit that fits it: B, kB, MB or GB
// (1024-based), with one decimal below 10 units ("3.4 MB", "512 kB").
func FormatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", max64(n, 0))
	}
	value := float64(n)
	for _, unit := range []string{"kB", "MB", "GB", "TB"} {
		value /= 1024
		if value < 1024 || unit == "TB" {
			if value < 10 {
				return fmt.Sprintf("%.1f %s", value, unit)
			}
			return fmt.Sprintf("%.0f %s", value, unit)
		}
	}
	return ""
}

// FormatByteRate renders a transfer speed like the call captions: Bps, kBps,
// MBps or GBps (bytes per second).
func FormatByteRate(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return "0 Bps"
	}
	s := FormatBytes(int64(bytesPerSecond + 0.5))
	return s + "ps"
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// transferRate measures the current transfer speed over the last couple of
// seconds, so it follows changes but does not jump with every chunk.
type transferRate struct {
	samples []rateSample
}

type rateSample struct {
	at   time.Time
	sent int64
}

const transferRateWindow = 2 * time.Second

func (r *transferRate) reset() { r.samples = r.samples[:0] }

// observe records progress and returns the speed in bytes per second (0
// until there is a span to measure).
func (r *transferRate) observe(sent int64, now time.Time) float64 {
	if n := len(r.samples); n > 0 && sent < r.samples[n-1].sent {
		r.reset() // a new transfer
	}
	r.samples = append(r.samples, rateSample{at: now, sent: sent})
	cut := 0
	for cut < len(r.samples)-2 && now.Sub(r.samples[cut+1].at) >= transferRateWindow {
		cut++
	}
	r.samples = r.samples[cut:]
	first := r.samples[0]
	span := now.Sub(first.at)
	if span < 100*time.Millisecond {
		return 0
	}
	return float64(sent-first.sent) / span.Seconds()
}
