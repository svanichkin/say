package logs

// DEBUG-TEMP: everything in this file is temporary tracing for unreliable
// calls (the log's recent lines are kept so that they can be sent to the
// peer; the peer's arrive into a file of their own). Remove once calls are
// reliable, with the log forwarding in network/tcp/logforward.go.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// The ring keeps the latest lines of the log, numbered from the start.
const (
	ringMaxLines = 250000 // frame-by-frame traces: a few minutes of a call
	ringMaxBytes = 40 << 20
)

var ring struct {
	sync.Mutex
	lines [][]byte
	first uint64 // the number of lines[0]
	bytes int
}

// BootID tells this run's lines apart from another's in the peer's file.
var BootID = func() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}()

// Tap returns a writer that writes to w and keeps every line in the ring.
// log.Logger writes one line per Write.
func Tap(w io.Writer) io.Writer { return &tap{w: w} }

type tap struct{ w io.Writer }

func (t *tap) Write(p []byte) (int, error) {
	keep(p)
	if t.w == nil {
		return len(p), nil
	}
	return t.w.Write(p)
}

func keep(p []byte) {
	ring.Lock()
	defer ring.Unlock()
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		line := p
		if i >= 0 {
			line = p[:i+1]
		}
		p = p[len(line):]
		ring.lines = append(ring.lines, append([]byte(nil), line...))
		ring.bytes += len(line)
	}
	for len(ring.lines) > ringMaxLines || ring.bytes > ringMaxBytes {
		ring.bytes -= len(ring.lines[0])
		ring.lines[0] = nil
		ring.lines = ring.lines[1:]
		ring.first++
	}
}

// Oldest is the number of the oldest line kept.
func Oldest() uint64 {
	ring.Lock()
	defer ring.Unlock()
	return ring.first
}

// Since returns the lines from number from on, at most max bytes of them
// (at least one line when there is one), and the number to go on from.
// Lines dropped from the ring before they were taken are noted.
func Since(from uint64, max int) (chunk []byte, next uint64) {
	ring.Lock()
	defer ring.Unlock()
	if from < ring.first {
		chunk = fmt.Appendf(chunk, "--- %d lines of this log were dropped before they could be sent ---\n", ring.first-from)
		from = ring.first
	}
	i := int(from - ring.first)
	for ; i < len(ring.lines); i++ {
		if len(chunk) > 0 && len(chunk)+len(ring.lines[i]) > max {
			break
		}
		chunk = append(chunk, ring.lines[i]...)
	}
	return chunk, ring.first + uint64(i)
}

// The peer's log, as it arrives during calls, goes to a file of its own.
var peerLog struct {
	sync.Mutex
	path string
	f    *os.File
}

// SetPeerLogPath sets the file for the peer's log ("" for none).
func SetPeerLogPath(path string) {
	peerLog.Lock()
	defer peerLog.Unlock()
	peerLog.path = path
}

// PeerLogPath is the file the peer's log goes to ("" for none).
func PeerLogPath() string {
	peerLog.Lock()
	defer peerLog.Unlock()
	return peerLog.path
}

// WritePeer appends lines of the peer's log: queued for a goroutine of its
// own, so that the control stream's reader (which gets the chunks) never
// waits for the disk.
func WritePeer(data []byte) {
	peerWrites.Do(func() { go writePeerLoop() })
	select {
	case peerQueue <- data:
	default: // the disk is far behind: this chunk is dropped
		peerDropped.Add(1)
	}
}

var (
	peerWrites  sync.Once
	peerQueue   = make(chan []byte, 256)
	peerDropped atomic.Int64
)

func writePeerLoop() {
	for data := range peerQueue {
		peerLog.Lock()
		if peerLog.path != "" {
			if peerLog.f == nil {
				f, err := os.OpenFile(peerLog.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
				if err == nil {
					peerLog.f = f
					fmt.Fprintf(f, "=== %s: the peer's log follows (each call sends what it kept) ===\n", time.Now().Format(time.RFC3339))
				}
			}
			if peerLog.f != nil {
				_, _ = peerLog.f.Write(data)
				if n := peerDropped.Swap(0); n > 0 {
					fmt.Fprintf(peerLog.f, "--- %d chunks of the peer's log were dropped (the disk fell behind) ---\n", n)
				}
			}
		}
		peerLog.Unlock()
	}
}
