package tcp

import (
	"context"
	"encoding/binary"
	"log"
	"net"

	"github.com/svanichkin/say/codec"
)

// TermSize describes a viewport request in character cells.
type TermSize struct {
	Cols int
	Rows int
}

// TermSizeSync wires terminal size exchange between the transport layer and
// whichever component owns the UI. Local carries outbound sizes, Initial seeds
// the first frame, and OnPeer receives sizes learned from the remote side.
type TermSizeSync struct {
	Local   <-chan TermSize
	Initial *TermSize
	OnPeer  func(cols, rows int)
}

func (tss *TermSizeSync) notifyPeer(cols, rows int) {
	if tss == nil || tss.OnPeer == nil {
		return
	}
	if cols <= 0 || rows <= 0 {
		return
	}
	tss.OnPeer(cols, rows)
}

// sendTermSizeFrame encodes the local viewport request (cols, rows) into a frameTermSize
// control frame and sends it to the peer. Dimensions are clamped to 16-bit.
func sendTermSizeFrame(conn net.Conn, cols, rows int) error {
	if conn == nil {
		return nil
	}
	payload, ok := encodeTermSizeFrame(cols, rows)
	if !ok {
		return nil
	}
	return writeFrameSafe(conn, frameTermSize, payload)
}

// encodeTermSizeFrame кодирует (cols, rows) в payload для frameTermSize с магическим префиксом "TEXT".
func encodeTermSizeFrame(cols, rows int) ([]byte, bool) {
	if cols <= 0 || rows <= 0 {
		return nil, false
	}
	if cols > 0xFFFF {
		cols = 0xFFFF
	}
	if rows > 0xFFFF {
		rows = 0xFFFF
	}
	buf := make([]byte, len(codec.PayloadMagic)+4)
	copy(buf, codec.PayloadMagic)
	offset := len(codec.PayloadMagic)
	binary.BigEndian.PutUint16(buf[offset:offset+2], uint16(cols))
	binary.BigEndian.PutUint16(buf[offset+2:offset+4], uint16(rows))
	return buf, true
}

// decodeTermSizeFrame decodes a frameTermSize payload into terminal dimensions (cols, rows).
// It expects the payloadMagic prefix followed by two big-endian uint16 values.
func decodeTermSizeFrame(payload []byte) (cols, rows int, ok bool) {
	if len(payload) != len(codec.PayloadMagic)+4 {
		return 0, 0, false
	}
	if string(payload[:len(codec.PayloadMagic)]) != codec.PayloadMagic {
		return 0, 0, false
	}
	offset := len(codec.PayloadMagic)
	cols = int(binary.BigEndian.Uint16(payload[offset : offset+2]))
	rows = int(binary.BigEndian.Uint16(payload[offset+2 : offset+4]))
	return cols, rows, true
}

// startTermSizeSender reads terminal sizes from the provided channel and
// forwards them to the peer over the given connection.
// The network layer no longer knows how sizes are produced.
func startTermSizeSender(
	ctx context.Context,
	conn net.Conn,
	sizes <-chan TermSize,
	initial *TermSize,
) func() {
	if conn == nil || sizes == nil {
		return func() {}
	}

	send := func(cols, rows int) {
		payload, ok := encodeTermSizeFrame(cols, rows)
		if !ok {
			return
		}
		if err := writeFrameSafe(conn, frameTermSize, payload); err != nil {
			log.Printf("[term] failed to send size %dx%d: %v", cols, rows, err)
		}
	}

	done := make(chan struct{})

	// optional initial size
	if initial != nil {
		send(initial.Cols, initial.Rows)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case ts, ok := <-sizes:
				if !ok {
					return
				}
				send(ts.Cols, ts.Rows)
			}
		}
	}()

	return func() {
		close(done)
	}
}
