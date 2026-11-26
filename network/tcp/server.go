package tcp

import (
	"bufio"
	"context"
	"fmt"
	"github.com/svanichkin/say/lang"
	"log"
	"net"
	"strings"
	"time"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

// StartSignalServerTCP accepts incoming P2P control connections on ln.
// For each new connection it performs a simple text handshake and then hands the
// connection off to handleIncoming in its own goroutine. The returned function
// shuts down the listener and stops accepting new connections.
// nameOf names a caller by its address ("": the name it gives).
func StartSignalServerTCP(ln net.Listener, nameOf func(host string) string, listenPort int, termSync *TermSizeSync, mediaFactory MediaSessionFactory) (func() error, error) {
	stop := make(chan struct{})
	owner := calls
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
			go serverHandleIncoming(conn, owner, nameOf, termSync, mediaFactory)
		}
	}()

	return func() error {
		close(stop)
		err := ln.Close()
		owner.hangupActive() // end the current call, telling the peer
		return err
	}, nil
}

// serverHandleIncoming processes a single accepted P2P control connection.
// It performs the HELLO/OK handshake, starts optional video and UDP voice sessions,
// wires up keepalive and terminal size exchange, and then enters the frame receive loop.
func serverHandleIncoming(conn net.Conn, owner *callOwner, nameOf func(host string) string, termSync *TermSizeSync, mediaFactory MediaSessionFactory) {
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

	// This reader serves the connection from the handshake until it closes.
	br := bufio.NewReader(conn)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := br.ReadString('\n')
	rem := "unknown"
	if ra := conn.RemoteAddr(); ra != nil {
		rem = ra.String()
	}
	if err == nil && answerPing(conn, line) {
		return // whether we are here (presence.go): no call
	}
	logs.LogV("[p2p] incoming from %s", rem)
	if err != nil {
		log.Printf("[p2p] handshake read err from %s: %v", rem, err)
		return
	}
	conn.SetDeadline(time.Time{})
	peer := PeerInfo{Name: "unknown", Proto: 1, Min: 1, Legacy: true}
	if rest, ok := strings.CutPrefix(line, "HELLO "); ok {
		peer = parseHello(rest)
	}
	peerName := peer.Name
	logs.LogV("[p2p] handshake: HELLO from=%q raw=%q", peerName, strings.TrimSpace(line))
	// The caller's name in our contacts, if it is there.
	hostOnly := rem
	if h, _, err := net.SplitHostPort(rem); err == nil {
		hostOnly = h
	}
	friendLabel := peerName
	if nameOf != nil {
		if n := nameOf(strings.Trim(hostOnly, "[]")); n != "" {
			friendLabel = n
		}
	}
	logs.LogV("[p2p] call from %s (%s)", friendLabel, hostOnly)

	if !compatible(peer) {
		log.Printf("[p2p] rejected call from %s (%s): %s", friendLabel, hostOnly, strings.ReplaceAll(incompatibleText(peer, lang.En), "\n", " · "))
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(conn, helloLine(incompatibleReply, conf.HostnameOr("me")))
		return
	}

	// Only one call may own the shared UDP socket, devices and UI at a time.
	release, ok := owner.acquire(strings.Trim(hostOnly, "[]"), conn)
	if !ok {
		log.Printf("[p2p] rejected call from %s (%s): busy", friendLabel, hostOnly)
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprintf(conn, "%s %s\n", busyReply, conf.HostnameOr("me"))
		return
	}
	// Deferred before the media session, so it runs after the session stops.
	defer release()

	// A caller of 17 on waits while the call rings (ring.go).
	if rings(peer) {
		if !awaitAnswer(conn, br, friendLabel+"\n"+strings.Trim(hostOnly, "[]")) {
			log.Printf("[p2p] call from %s (%s) not answered", friendLabel, hostOnly)
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprint(conn, helloLine(declinedReply, conf.HostnameOr("me")))
			return
		}
	}

	// Reply OK and keep the socket open (this is our P2P channel for future audio)
	fmt.Fprint(conn, helloLine("OK", conf.HostnameOr("me")))
	pacer := newAdaptiveFilePacer()
	installChatActions(conn, pacer)
	defer ui.SetChatActions(nil, nil)
	var incoming *incomingFile
	defer abortIncoming(&incoming)
	notePeer(peer)
	sendCard(conn)

	// Start media session (audio/video) on incoming side, learning peer address from first packet.
	if mediaFactory != nil {
		defer startMediaAside(conn, "incoming, learning peer", func() (MediaSession, error) {
			return mediaFactory(strings.Trim(hostOnly, "[]"), nil, peer)
		})()
	} else {
		log.Printf("[media] transport is disabled")
	}

	tcpKA, stopKeepalive := startTCPKeepalive(ctx, conn, keepaliveInterval, keepaliveTimeout, true, pacer.active)
	if stopKeepalive != nil {
		defer stopKeepalive()
	}

	if termSync != nil && termSync.Local != nil {
		stopTermSize := startTermSizeSender(ctx, conn, termSync.Local, termSync.Initial, termSync.Current)
		defer stopTermSize()
	}
	defer startVideoCtl(ctx, conn)()

	serveControlFrames(br, controlSession{
		conn:      conn,
		keepalive: tcpKA,
		termSync:  termSync,
		pacer:     pacer,
		incoming:  &incoming,
		peerLabel: friendLabel,
		peerHost:  strings.Trim(hostOnly, "[]"),
	})
}
