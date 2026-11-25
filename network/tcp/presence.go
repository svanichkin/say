package tcp

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// Presence (protocol 16): whether Say runs at an address. In place of the
// handshake's HELLO the asker sends PING; Say answers PONG with its version
// and protocol and closes, no call begun and the line not taken. A Say of
// 15 or older takes PING for a peer too old and answers so: an answer all
// the same (online).

const (
	pingLine = "PING"
	pongLine = "PONG"
)

// answerPing answers a presence probe on a connection whose first line was
// line; false if it was not one (a call's HELLO).
func answerPing(conn interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
}, line string) bool {
	if strings.TrimSpace(line) != pingLine {
		return false
	}
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "%s %s %d\n", pongLine, LocalApp(), ProtocolVersion)
	return true
}

// Probe reports whether Say answers at host:port within timeout (dial is
// the call's own). A connection that comes up after the timeout is closed.
func Probe(host string, port int, dial TCPDialFunc, timeout time.Duration) bool {
	if dial == nil {
		return false
	}
	answered := make(chan bool, 1)
	go func() {
		conn, err := dial(host, port)
		if err != nil {
			answered <- false
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(timeout))
		if _, err := fmt.Fprintf(conn, "%s\n", pingLine); err != nil {
			answered <- false
			return
		}
		// Any line is Say's: PONG, or an older one's refusal.
		_, err = bufio.NewReader(conn).ReadString('\n')
		answered <- err == nil
	}()
	select {
	case ok := <-answered:
		return ok
	case <-time.After(timeout):
		return false
	}
}
