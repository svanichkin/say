package tcp

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

type TCPDialFunc func(host string, port int) (net.Conn, error)

// StartSignalClientTCP connects to peerAddr on the given port and performs
// the same HELLO/OK handshake as handleIncoming, using the provided TCP dialer.
// On success it returns a framedConn suitable for sending and receiving frames along with a done channel.
func StartSignalClientTCP(localName string, peerAddr string, port int, termSync *TermSizeSync, mediaFactory MediaSessionFactory, dial TCPDialFunc) (func() error, <-chan struct{}, error) {
	if dial == nil {
		return nil, nil, fmt.Errorf("tcp dialer is nil")
	}
	log.Printf("[p2p] dialing %s", prettyAddr(peerAddr, port))
	conn, err := dial(peerAddr, port)
	if err != nil {
		return nil, nil, err
	}
	// --- Socket tuning for outgoing connections ---
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(15 * time.Second)
	}
	// Send HELLO with our protocol version and voice codec.
	fmt.Fprint(conn, helloLine("HELLO", localName))
	logs.LogV("[p2p] sent HELLO %q", localName)
	// Expect OK
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// This reader serves the connection from the handshake until it closes:
	// frames sent right after OK may already be buffered in it.
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("handshake: %w", err)
	}
	logs.LogV("[p2p] handshake reply: %s", strings.TrimSpace(line))
	if strings.HasPrefix(line, busyReply) {
		conn.Close()
		return nil, nil, ErrPeerBusy
	}
	if rest, ok := strings.CutPrefix(line, incompatibleReply+" "); ok {
		conn.Close()
		return nil, nil, &IncompatibleError{Peer: parseHello(rest)}
	}
	rest, ok := strings.CutPrefix(line, "OK ")
	if !ok {
		conn.Close()
		return nil, nil, fmt.Errorf("unexpected reply: %q", strings.TrimSpace(line))
	}
	peer := parseHello(rest)
	if !compatible(peer) {
		_ = hangup(conn)
		return nil, nil, &IncompatibleError{Peer: peer}
	}
	logs.LogV("[p2p] connected: %s", strings.TrimSpace(line))

	// Wrap the TCP connection in a framedConn for safe framed writes.
	fc := &framedConn{Conn: conn}

	// Start client-side session handler in its own goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		clientHandleIncoming(fc, br, peerAddr, port, peer, termSync, mediaFactory)
	}()

	stop := func() error {
		return hangup(fc)
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
// It sets up optional video and voice streams, starts terminal size propagation,
// then parses control frames from br until the connection closes.
func clientHandleIncoming(conn net.Conn, br *bufio.Reader, host string, port int, peer PeerInfo, termSync *TermSizeSync, mediaFactory MediaSessionFactory) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer conn.Close()
	pacer := newAdaptiveFilePacer()
	installChatActions(conn, pacer)
	defer ui.SetChatActions(nil, nil)
	var incoming *incomingFile
	defer abortIncoming(&incoming)
	notePeer(peer)
	askPeerLog(conn) // DEBUG-TEMP

	tcpKA, stopKeepalive := startTCPKeepalive(sessionCtx, conn, keepaliveInterval, keepaliveTimeout, true, pacer.active)
	defer stopKeepalive()

	if termSync != nil && termSync.Local != nil {
		stopTerm := startTermSizeSender(sessionCtx, conn, termSync.Local, termSync.Initial, termSync.Current)
		defer stopTerm()
	}
	defer startKeyframeRequester(sessionCtx, conn)()

	if mediaFactory == nil {
		log.Printf("[media] transport is disabled")
	} else {
		raddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)))
		if err != nil {
			log.Printf("[media] invalid remote address %q: %v", host, err)
		} else {
			if sess, err := mediaFactory(strings.Trim(host, "[]"), raddr, peer); err != nil {
				log.Printf("[media] failed to start: %v", err)
			} else if sess != nil {
				defer sess.Stop()
				defer hangupWhenMediaEnds(sess, conn)()
				log.Printf("[media] started (outgoing) → %s", raddr.String())
			}
		}
	}

	serveControlFrames(br, controlSession{
		conn:      conn,
		keepalive: tcpKA,
		termSync:  termSync,
		pacer:     pacer,
		incoming:  &incoming,
		peerLabel: prettyAddr(host, port),
	})
}
