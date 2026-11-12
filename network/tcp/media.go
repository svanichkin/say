package tcp

import (
	"log"
	"net"
	"sync"
)

// MediaSession represents a running media transport (voice/video) that can be stopped.
type MediaSession interface {
	Stop() error
	// Done closes once the session has ended, whoever ended it.
	Done() <-chan struct{}
}

// hangupWhenMediaEnds hangs up the call if its media session ends while the
// call goes on (the speaker gone, a fatal socket error): the control
// connection alone would keep a call with no sound or picture, and the
// caller would not redial. The returned func stops watching (the call ends
// the session itself).
func hangupWhenMediaEnds(sess MediaSession, conn net.Conn) (stop func()) {
	quit := make(chan struct{})
	go func() {
		select {
		case <-quit:
		case <-sess.Done():
			select {
			case <-quit:
				return
			default:
			}
			log.Printf("[media] the call's sound and video ended by themselves: hanging up")
			_ = hangup(conn)
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(quit) }) }
}

// MediaSessionFactory starts an optional media session for a given peer.
// When remote is nil, the session should learn the peer from incoming traffic.
// peer is what the peer announced in the handshake (its voice codec).
type MediaSessionFactory func(peerHost string, remote *net.UDPAddr, peer PeerInfo) (MediaSession, error)
