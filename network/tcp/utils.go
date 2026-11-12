package tcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

// Frame type identifiers for the simple binary framing protocol used on top of the TCP connection.
// Each frame starts with a 1-byte type followed by a 2-byte big-endian payload length and the payload itself.
const (
	frameAudio      byte = 0x01
	frameVideo      byte = 0x02
	framePing       byte = 0x03
	framePong       byte = 0x04
	frameTermSize   byte = 0x05
	frameChat       byte = 0x06
	frameFileStart  byte = 0x07
	frameFileChunk  byte = 0x08
	frameFileEnd    byte = 0x09
	frameFileResult byte = 0x0a // receiver → sender: file saved or failed
	frameBye        byte = 0x0b // the peer is hanging up: end the call now
	frameKeyframe   byte = 0x0c // receiver → sender: a video frame was lost, send a keyframe
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
	t0 := time.Now() // DEBUG-TEMP: where a slow ping or pong loses its time
	fc.mu.Lock()
	defer fc.mu.Unlock()
	t1 := time.Now()
	err := writeFrame(fc.Conn, t, payload)
	if d := time.Since(t0); d > 5*time.Millisecond { // DEBUG-TEMP
		log.Printf("[debug] stream: writing a %s frame (%d B) took %v: %v waiting for the stream, %v writing",
			frameName(t), len(payload), d.Round(time.Millisecond), t1.Sub(t0).Round(time.Millisecond), time.Since(t1).Round(time.Millisecond))
	}
	return err
}

// frameName names a control frame type in the log (DEBUG-TEMP).
func frameName(t byte) string {
	switch t {
	case framePing:
		return "ping"
	case framePong:
		return "pong"
	case frameChat:
		return "chat"
	case frameFileStart, frameFileChunk, frameFileEnd, frameFileResult:
		return "file"
	case frameBye:
		return "bye"
	case frameKeyframe:
		return "keyframe"
	case frameTermSize:
		return "termsize"
	case frameLogWant, frameLog:
		return "log"
	}
	return fmt.Sprintf("0x%02x", t)
}

// writeFrame encodes and writes a single framed message as:
// [1 byte type][2 bytes big-endian length][payload bytes].
func writeFrame(w net.Conn, t byte, payload []byte) error {
	if len(payload) > 0xFFFF {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	hdr := []byte{t, byte(len(payload) >> 8), byte(len(payload))}
	if err := writeAll(w, hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		return writeAll(w, payload)
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
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
//
// The reader must stay blocking (no read deadlines): a timeout between the header
// and the end of the payload would drop the partial frame and desynchronize the stream.
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [3]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	t := h[0]
	ln := int(h[1])<<8 | int(h[2])
	buf := make([]byte, ln)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return t, buf, nil
}

// controlSession is the state shared by both sides of a control connection.
type controlSession struct {
	conn      net.Conn
	keepalive *tcpKeepalive
	termSync  *TermSizeSync
	pacer     *adaptiveFilePacer
	incoming  **incomingFile
	peerLabel string
}

// serveControlFrames parses the control stream until the connection closes.
// br must be the same reader that consumed the handshake line, because it may
// already hold the first frames the peer sent right after the handshake.
// Reads block without deadlines; the session ends when the connection is
// closed (by stop, a keepalive timeout or the peer).
func serveControlFrames(br *bufio.Reader, s controlSession) {
	// DEBUG-TEMP: how the control stream went, logged when it ends.
	started, last := time.Now(), time.Now()
	counts := map[byte]int{}
	end := func(why string) {
		log.Printf("[debug] control stream with %s ended after %v: %s; last frame %v before; frames %v",
			s.peerLabel, time.Since(started).Round(time.Millisecond), why, time.Since(last).Round(time.Millisecond), counts)
	}
	var handling time.Time // DEBUG-TEMP: a slow handler holds up the pongs behind it
	var handlingType byte
	for {
		if !handling.IsZero() { // DEBUG-TEMP
			if d := time.Since(handling); d > 5*time.Millisecond {
				log.Printf("[debug] stream: handling a %s frame from %s took %v", frameName(handlingType), s.peerLabel, d.Round(time.Millisecond))
			}
			handling = time.Time{}
		}
		t, payload, err := readFrame(br)
		if err != nil {
			if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				log.Printf("[p2p] readFrame error from %s: %v", s.peerLabel, err)
			}
			end(fmt.Sprintf("read: %v", err)) // DEBUG-TEMP
			return
		}
		if gap := time.Since(last); gap > 3*time.Second { // DEBUG-TEMP
			log.Printf("[debug] control stream from %s was silent for %v", s.peerLabel, gap.Round(time.Millisecond))
		}
		last = time.Now()
		counts[t]++
		handling, handlingType = time.Now(), t // DEBUG-TEMP: checked at the top of the loop
		switch t {
		case frameLogWant: // DEBUG-TEMP
			sendLog(s.conn, s.peerLabel)
		case frameLog: // DEBUG-TEMP
			receiveLog(payload)
		case frameBye:
			log.Printf("[p2p] %s hung up", s.peerLabel)
			end("the peer hung up") // DEBUG-TEMP
			return
		case frameChat, frameFileStart, frameFileChunk, frameFileEnd, frameFileResult:
			handleChatFrame(s.conn, t, payload, s.incoming)
		case frameKeyframe:
			ui.NotePeerKeyframeRequest()
		case frameTermSize:
			if cols, rows, ok := decodeTermSizeFrame(payload); ok && s.termSync != nil {
				s.termSync.notifyPeer(cols, rows)
			}
		case framePing:
			if s.keepalive == nil {
				continue
			}
			if pong := s.keepalive.pongPayload(payload); pong != nil {
				if err := writeFrameSafe(s.conn, framePong, pong); err != nil {
					log.Printf("[keepalive] pong send failed: %v", err)
					return
				}
			}
		case framePong:
			if s.keepalive == nil {
				continue
			}
			if rtt, loss, ok := s.keepalive.acceptPong(payload); ok {
				ui.UpdatePeerLatency(rtt)
				s.pacer.observe(rtt, loss)
				logs.LogV("[keepalive] pong received: RTT=%s peer audio loss=%.1f%%", rtt, loss*100)
			}
		default:
			// Audio and video travel over UDP; ignore unknown frame types.
		}
	}
}

// hangupFlush gives the in-process network stack (Yggdrasil) time to deliver
// the goodbye before the node shuts down with the process.
const hangupFlush = 300 * time.Millisecond

// hangup tells the peer the call is over and closes the connection, so the
// peer ends the call at once instead of waiting for the keepalive timeout.
func hangup(conn net.Conn) error {
	if conn == nil {
		return nil
	}
	_ = conn.SetWriteDeadline(time.Now().Add(hangupFlush))
	sent := writeFrameSafe(conn, frameBye, nil) == nil
	err := conn.Close()
	if sent {
		time.Sleep(hangupFlush)
	}
	return err
}

// startKeyframeRequester forwards the video receiver's keyframe requests
// (ui.RequestKeyframe) to the peer. A peer that does not know the frame
// ignores it, as every unknown control frame.
func startKeyframeRequester(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ui.KeyframeRequests():
				if err := writeFrameSafe(conn, frameKeyframe, nil); err != nil {
					log.Printf("[p2p] keyframe request failed: %v", err)
				}
			}
		}
	}()
	return func() { close(done) }
}
