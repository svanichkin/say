package tcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
	"strings"
	"time"
)

// StartSignalServerTCP accepts incoming P2P control connections on ln.
// For each new connection it performs a simple text handshake and then hands the
// connection off to handleIncoming in its own goroutine. The returned function
// shuts down the listener and stops accepting new connections.
func StartSignalServerTCP(ln net.Listener, known []conf.Friend, listenPort int, termSync *TermSizeSync, mediaFactory MediaSessionFactory) (func() error, error) {
	stop := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
				}
				log.Printf("[p2p] accept error: %v", err)
				continue
			}
			// --- Socket tuning for incoming connections ---
			if tc, ok := conn.(*net.TCPConn); ok {
				tc.SetNoDelay(true)
				tc.SetKeepAlive(true)
				tc.SetKeepAlivePeriod(15 * time.Second)
			}
			conn = &framedConn{Conn: conn}
			if ra := conn.RemoteAddr(); ra != nil {
				logs.LogV("[p2p] accepted from %s", ra)
			} else {
				logs.LogV("[p2p] accepted; waiting first packet to learn peer")
			}
			go serverHandleIncoming(conn, known, termSync, mediaFactory)
		}
	}()

	return func() error { close(stop); return ln.Close() }, nil
}

// serverHandleIncoming processes a single accepted P2P control connection.
// It performs the HELLO/OK handshake, starts optional video and UDP voice sessions,
// wires up keepalive and terminal size exchange, and then enters the frame receive loop.
func serverHandleIncoming(conn net.Conn, known []conf.Friend, termSync *TermSizeSync, mediaFactory MediaSessionFactory) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	defer func() {
		rem := "unknown"
		if ra := conn.RemoteAddr(); ra != nil {
			rem = ra.String()
		}
		logs.LogV("[p2p] connection closed: %s", rem)
		conn.Close()
	}()

	// Simple line-based handshake: we expect HELLO <name>\n

	br := bufio.NewReader(conn) // use default buffer size
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := br.ReadString('\n')
	rem := "unknown"
	if ra := conn.RemoteAddr(); ra != nil {
		rem = ra.String()
	}
	logs.LogV("[p2p] incoming from %s", rem)
	if err != nil {
		log.Printf("[p2p] handshake read err from %s: %v", rem, err)
		return
	}
	conn.SetDeadline(time.Time{})
	line = strings.TrimSpace(line)
	peerName := "unknown"
	if strings.HasPrefix(line, "HELLO ") {
		peerName = strings.TrimSpace(strings.TrimPrefix(line, "HELLO "))
	}
	logs.LogV("[p2p] handshake: HELLO from=%q raw=%q", peerName, strings.TrimSpace(line))
	// Identify friend by address (host part) if possible
	hostOnly := rem
	if h, _, err := net.SplitHostPort(rem); err == nil {
		hostOnly = h
	}
	friendLabel := peerName
	for _, f := range known {
		if strings.EqualFold(f.Address, strings.Trim(hostOnly, "[]")) {
			friendLabel = f.Name
			break
		}
	}
	logs.LogV("[p2p] call from %s (%s)", friendLabel, hostOnly)

	// Reply OK and keep the socket open (this is our P2P channel for future audio)
	fmt.Fprintf(conn, "OK %s\n", conf.HostnameOr("me"))

	// Start media session (audio/video) on incoming side, learning peer address from first packet.
	if mediaFactory != nil {
		if sess, err := mediaFactory(nil); err != nil {
			log.Printf("[media] start incoming failed: %v", err)
		} else if sess != nil {
			defer sess.Stop()
			log.Printf("[media] started (incoming, learning peer)")
		}
	} else {
		log.Printf("[media] transport is disabled")
	}

	tcpKA, stopKeepalive := startTCPKeepalive(ctx, conn, 2*time.Second, 6*time.Second, true)
	if stopKeepalive != nil {
		defer stopKeepalive()
	}

	if termSync != nil && termSync.Local != nil {
		stopTermSize := startTermSizeSender(ctx, conn, termSync.Local, termSync.Initial)
		defer stopTermSize()
	}

	brFrames := bufio.NewReader(conn)
	for {
		t, payload, err := readFrame(brFrames)
		if err != nil {
			if err != io.EOF {
				log.Printf("[p2p] readFrame error from %s: %v", friendLabel, err)
			}
			return
		}
		if tcpKA != nil {
			tcpKA.touch()
		}
		switch t {
		case frameVideo:
			// ignore here; video handled via UDP
		case frameAudio:
			// ignore here; audio handled via UDP
		case frameTermSize:
			if cols, rows, ok := decodeTermSizeFrame(payload); ok && termSync != nil {
				termSync.notifyPeer(cols, rows)
			}
		case frameTick:
			if latency, ok := decodeTickLatency(payload); ok {
				ui.UpdatePeerLatency(latency)
				logs.LogV("[keepalive] tick received (server data) latency=%s", latency)
			} else {
				logs.LogV("[keepalive] tick received (server data)")
			}
		default:
			// unknown frame type; ignore
		}
	}
}
