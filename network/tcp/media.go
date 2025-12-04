package tcp

import "net"

// MediaSession represents a running media transport (voice/video) that can be stopped.
type MediaSession interface {
	Stop() error
}

// MediaSessionFactory starts an optional media session for a given peer.
// When remote is nil, the session should learn the peer from incoming traffic.
type MediaSessionFactory func(remote *net.UDPAddr) (MediaSession, error)
