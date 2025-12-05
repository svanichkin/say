package tcp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
)

// Frame type identifiers for the simple binary framing protocol used on top of the TCP connection.
// Each frame starts with a 1-byte type followed by a 2-byte big-endian payload length and the payload itself.
const (
	frameAudio    byte = 0x01
	frameVideo    byte = 0x02
	frameTick     byte = 0x03
	frameTermSize byte = 0x05
)

// framedConn wraps a net.Conn and serializes writes so that higher-level framed messages
// are not interleaved when multiple goroutines share the same connection.
type framedConn struct {
	net.Conn
	mu sync.Mutex
}

// WriteFrame serializes framed writes across goroutines sharing the same connection.
func (fc *framedConn) WriteFrame(t byte, payload []byte) error {
	if fc == nil {
		return fmt.Errorf("framedConn is nil")
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return writeFrame(fc.Conn, t, payload)
}

// writeFrame encodes and writes a single framed message as:
// [1 byte type][2 bytes big-endian length][payload bytes].
func writeFrame(w net.Conn, t byte, payload []byte) error {
	if len(payload) > 0xFFFF {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	hdr := []byte{t, byte(len(payload) >> 8), byte(len(payload))}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// writeFrameSafe sends a framed message over conn, preferring a WriteFrame method
// when available (for framedConn) to ensure serialized access, and falling back to writeFrame otherwise.
func writeFrameSafe(conn net.Conn, t byte, payload []byte) error {
	if fw, ok := conn.(interface {
		WriteFrame(t byte, payload []byte) error
	}); ok {
		return fw.WriteFrame(t, payload)
	}
	return writeFrame(conn, t, payload)
}

// readFrame reads a single framed message from the buffered reader according to the
// [type][length][payload] format. It returns the frame type, payload bytes, or an error.
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	h, err := r.Peek(3)
	if err != nil {
		return 0, nil, err
	}
	_, _ = r.Discard(3)
	t := h[0]
	ln := int(h[1])<<8 | int(h[2])
	buf := make([]byte, ln)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return t, buf, nil
}
