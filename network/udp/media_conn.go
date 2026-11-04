package udp

import (
	"net"
	"time"
)

// Deadlines belong to a socket, not a goroutine. Serialize writes so video
// cannot extend a blocked audio write's deadline (or vice versa). The timeout
// bounds local transport backpressure; it is unrelated to peer RTT.
type mediaPacketConn struct {
	net.PacketConn
	stop   <-chan struct{}
	writer chan struct{}
}

func newMediaPacketConn(pc net.PacketConn, stop <-chan struct{}) *mediaPacketConn {
	return &mediaPacketConn{PacketConn: pc, stop: stop, writer: make(chan struct{}, 1)}
}

func (c *mediaPacketConn) WriteTo(data []byte, addr net.Addr) (int, error) {
	select {
	case <-c.stop:
		return 0, net.ErrClosed
	default:
	}
	select {
	case c.writer <- struct{}{}:
	case <-c.stop:
		return 0, net.ErrClosed
	}
	defer func() { <-c.writer }()
	select {
	case <-c.stop:
		return 0, net.ErrClosed
	default:
	}
	if err := c.PacketConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return 0, err
	}
	return c.PacketConn.WriteTo(data, addr)
}
