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

// startMediaAside starts the call's sound and video beside its control
// stream, not before it: opening the microphone, the speaker and the camera
// can take seconds, and pings left unanswered that long end the call. The
// returned stop waits for the start to finish, then ends the session.
func startMediaAside(conn net.Conn, what string, start func() (MediaSession, error)) (stop func()) {
	type started struct {
		sess      MediaSession
		stopWatch func()
	}
	ready := make(chan started, 1)
	go func() {
		sess, err := start()
		if err != nil {
			log.Printf("[media] %s failed to start: %v", what, err)
			ready <- started{}
			return
		}
		if sess == nil {
			ready <- started{}
			return
		}
		log.Printf("[media] started (%s)", what)
		ready <- started{sess, hangupWhenMediaEnds(sess, conn)}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			if s := <-ready; s.sess != nil {
				s.stopWatch()
				_ = s.sess.Stop()
			}
		})
	}
}

// MediaSessionFactory starts an optional media session for a given peer.
// When remote is nil, the session should learn the peer from incoming traffic.
// peer is what the peer announced in the handshake (its voice codec).
type MediaSessionFactory func(peerHost string, remote *net.UDPAddr, peer PeerInfo) (MediaSession, error)
