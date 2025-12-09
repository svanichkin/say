package tcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
	"strings"
	"time"

	ygg "github.com/svanichkin/ygg"
)

// StartSignalClientTCP connects to peerAddr (Yggdrasil IPv6) on the given port and performs
// the same HELLO/OK handshake as handleIncoming, using the Yggdrasil TCP dialer.
// On success it returns a framedConn suitable for sending and receiving frames along with a done channel.
func StartSignalClientTCP(localName string, peerAddr string, port int, termSync *TermSizeSync, mediaFactory MediaSessionFactory) (func() error, <-chan struct{}, error) {
	log.Printf("[p2p] dialing %s", prettyAddr(peerAddr, port))
	conn, err := ygg.DialTCP(peerAddr, port)
	if err != nil {
		return nil, nil, err
	}
	// --- Socket tuning for outgoing connections ---
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(15 * time.Second)
	}
	// Send HELLO
	fmt.Fprintf(conn, "HELLO %s\n", localName)
	logs.LogV("[p2p] sent HELLO %q", localName)
	// Expect OK
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn) // use default buffer size for handshake
	line, err := br.ReadString('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("handshake: %w", err)
	}
	logs.LogV("[p2p] handshake OK: %s", strings.TrimSpace(line))
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, nil, fmt.Errorf("unexpected reply: %q", strings.TrimSpace(line))
	}
	logs.LogV("[p2p] connected: %s", strings.TrimSpace(line))

	// Wrap the TCP connection in a framedConn for safe framed writes.
	fc := &framedConn{Conn: conn}

	// Start client-side session handler in its own goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		clientHandleIncoming(fc, peerAddr, port, termSync, mediaFactory)
	}()

	stop := func() error {
		return fc.Close()
	}
	return stop, done, nil
}

func prettyAddr(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// clientHandleIncoming drives a single outgoing P2P session over an established control connection.
// It sets up optional video and voice streams, starts terminal size propagation, and then
// either enters the video receive loop or waits for the control connection to close.
func clientHandleIncoming(conn net.Conn, host string, port int, termSync *TermSizeSync, mediaFactory MediaSessionFactory) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer conn.Close()

	tcpKA, stopKeepalive := startTCPKeepalive(sessionCtx, conn, 2*time.Second, 6*time.Second, true)
	defer stopKeepalive()

	if termSync != nil && termSync.Local != nil {
		stopTerm := startTermSizeSender(sessionCtx, conn, termSync.Local, termSync.Initial)
		defer stopTerm()
	}

	if mediaFactory == nil {
		log.Printf("[media] transport is disabled")
	} else {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil {
			log.Printf("[media] invalid remote address %q", host)
		} else {
			raddr := &net.UDPAddr{IP: ip, Port: port}
			if sess, err := mediaFactory(raddr); err != nil {
				log.Printf("[media] failed to start: %v", err)
			} else if sess != nil {
				defer sess.Stop()
				log.Printf("[media] started (outgoing) → %s", raddr.String())
			}
		}
	}

	buf := make([]byte, 4096)
	var deadlineConn interface {
		SetReadDeadline(time.Time) error
	}
	if dc, ok := conn.(interface {
		SetReadDeadline(time.Time) error
	}); ok {
		deadlineConn = dc
	}

	// When keepalive is enabled, read framed messages so we can respond to ping/pong and handle control frames.
	if tcpKA != nil {
		br := bufio.NewReader(conn)
		for {
			if deadlineConn != nil {
				if err := deadlineConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
					log.Printf("[p2p] set deadline error: %v", err)
				}
			}

			t, payload, err := readFrame(br)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if sessionCtx != nil {
						select {
						case <-sessionCtx.Done():
							return
						default:
						}
					}
					continue
				}
				if err != io.EOF {
					log.Printf("[p2p] connection error: %v", err)
				}
				return
			}
			tcpKA.touch()
			if deadlineConn != nil {
				_ = deadlineConn.SetReadDeadline(time.Time{})
			}
			switch t {
			case frameTick:
				if latency, ok := decodeTickLatency(payload); ok {
					ui.UpdatePeerLatency(latency)
					logs.LogV("[keepalive] tick received (client ctl) latency=%s", latency)
				} else {
					logs.LogV("[keepalive] tick received (client ctl)")
				}
			case frameTermSize:
				if cols, rows, ok := decodeTermSizeFrame(payload); ok && termSync != nil {
					termSync.notifyPeer(cols, rows)
				}
			default:
				// ignore other frames on control channel
			}
		}
	}

	for {
		if deadlineConn != nil {
			if err := deadlineConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
				log.Printf("[p2p] set deadline error: %v", err)
			}
		}

		if _, err := conn.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if sessionCtx != nil {
					select {
					case <-sessionCtx.Done():
						return
					default:
					}
				}
				continue
			}
			if err != io.EOF {
				log.Printf("[p2p] connection error: %v", err)
			}
			return
		}
	}
}
