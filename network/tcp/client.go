package tcp

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

type TCPDialFunc func(host string, port int) (net.Conn, error)

// StartSignalClientTCP connects to peerAddr on the given port and performs
// the same HELLO/OK handshake as handleIncoming, using the provided TCP dialer.
// On success it returns a framedConn suitable for sending and receiving frames along with a done channel.
//
// peerHungUp reports, once done is closed, whether the peer hung up (its
// bye) rather than the line dropping.
//
// ctx ends the dial and the handshake (the line closes: the one called
// stops ringing); ringing is told when the call rings there (protocol 17),
// the answer awaited up to ringTimeout. The caller's ring is the caller's
// to play (main.go).
func StartSignalClientTCP(ctx context.Context, localName string, peerAddr string, port int, termSync *TermSizeSync, mediaFactory MediaSessionFactory, dial TCPDialFunc, ringing func()) (stop func() error, done <-chan struct{}, peerHungUp func() bool, err error) {
	if dial == nil {
		return nil, nil, nil, fmt.Errorf("tcp dialer is nil")
	}
	log.Printf("[p2p] dialing %s", prettyAddr(peerAddr, port))
	conn, err := dial(peerAddr, port)
	if err != nil {
		return nil, nil, nil, err
	}
	// --- Socket tuning for outgoing connections ---
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(15 * time.Second)
	}
	// Cancelled: the line closes, whatever it waits for.
	unwatch := context.AfterFunc(ctx, func() { conn.Close() })
	defer unwatch()
	// Send HELLO with our protocol version and voice codec.
	fmt.Fprint(conn, helloLine("HELLO", localName))
	logs.LogV("[p2p] sent HELLO %q", localName)
	// Expect OK
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// This reader serves the connection from the handshake until it closes:
	// frames sent right after OK may already be buffered in it.
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err == nil && strings.HasPrefix(line, ringReply+" ") { // it rings there: the answer
		logs.LogV("[p2p] ringing")
		if ringing != nil {
			ringing()
		}
		conn.SetReadDeadline(time.Now().Add(ringTimeout))
		line, err = br.ReadString('\n')
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			conn.Close()
			return nil, nil, nil, ErrNoAnswer
		}
	}
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
		return nil, nil, nil, fmt.Errorf("handshake: %w", err)
	}
	logs.LogV("[p2p] handshake reply: %s", strings.TrimSpace(line))
	if strings.HasPrefix(line, busyReply) {
		conn.Close()
		return nil, nil, nil, ErrPeerBusy
	}
	if strings.HasPrefix(line, declinedReply) {
		conn.Close()
		return nil, nil, nil, ErrDeclined
	}
	if rest, ok := strings.CutPrefix(line, incompatibleReply+" "); ok {
		conn.Close()
		return nil, nil, nil, &IncompatibleError{Peer: parseHello(rest)}
	}
	rest, ok := strings.CutPrefix(line, "OK ")
	if !ok {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("unexpected reply: %q", strings.TrimSpace(line))
	}
	peer := parseHello(rest)
	if !compatible(peer) {
		_ = hangup(conn)
		return nil, nil, nil, &IncompatibleError{Peer: peer}
	}
	logs.LogV("[p2p] connected: %s", strings.TrimSpace(line))

	// Wrap the TCP connection in a framedConn for safe framed writes.
	fc := &framedConn{Conn: conn}

	// One call at a time: a call that came in meanwhile keeps the line.
	release, ok := calls.acquire(strings.Trim(peerAddr, "[]"), fc)
	if !ok {
		_ = hangup(fc)
		return nil, nil, nil, ErrInCall
	}

	// Start client-side session handler in its own goroutine.
	ended := make(chan struct{})
	var bye atomic.Bool
	go func() {
		defer close(ended)
		defer release()
		clientHandleIncoming(fc, br, peerAddr, port, peer, termSync, mediaFactory, &bye)
	}()

	stop = func() error {
		return hangup(fc)
	}
	return stop, ended, bye.Load, nil
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
func clientHandleIncoming(conn net.Conn, br *bufio.Reader, host string, port int, peer PeerInfo, termSync *TermSizeSync, mediaFactory MediaSessionFactory, peerBye *atomic.Bool) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer conn.Close()
	pacer := newAdaptiveFilePacer()
	installChatActions(conn, pacer)
	defer ui.SetChatActions(nil, nil)
	var incoming *incomingFile
	defer abortIncoming(&incoming)
	notePeer(peer)
	sendCard(conn)
	askPeerLog(conn) // DEBUG-TEMP

	tcpKA, stopKeepalive := startTCPKeepalive(sessionCtx, conn, keepaliveInterval, keepaliveTimeout, true, pacer.active)
	defer stopKeepalive()

	if termSync != nil && termSync.Local != nil {
		stopTerm := startTermSizeSender(sessionCtx, conn, termSync.Local, termSync.Initial, termSync.Current)
		defer stopTerm()
	}
	defer startVideoCtl(sessionCtx, conn)()

	if mediaFactory == nil {
		log.Printf("[media] transport is disabled")
	} else {
		raddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)))
		if err != nil {
			log.Printf("[media] invalid remote address %q: %v", host, err)
		} else {
			defer startMediaAside(conn, "outgoing → "+raddr.String(), func() (MediaSession, error) {
				return mediaFactory(strings.Trim(host, "[]"), raddr, peer)
			})()
		}
	}

	serveControlFrames(br, controlSession{
		conn:      conn,
		keepalive: tcpKA,
		termSync:  termSync,
		pacer:     pacer,
		incoming:  &incoming,
		peerLabel: prettyAddr(host, port),
		peerHost:  strings.Trim(host, "[]"),
		peerBye:   peerBye,
	})
}
