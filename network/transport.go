package network

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

type TransportKind string

const (
	TransportYggdrasil TransportKind = "ygg"
	TransportPlain     TransportKind = "plain"
)

func (k TransportKind) String() string {
	return string(k)
}

func ParseTransportKind(raw string) (TransportKind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "ygg", "yggdrasil":
		return TransportYggdrasil, nil
	case "plain", "ip", "tcp", "tcpudp", "udp":
		return TransportPlain, nil
	default:
		return "", fmt.Errorf("unsupported transport %q", raw)
	}
}

type Transport interface {
	Kind() TransportKind
	LocalAddr() string
	WaitReady(ctx context.Context, timeout time.Duration) error
	ListenTCP(port int) (net.Listener, error)
	ListenUDP(port int) (net.PacketConn, error)
	DialTCP(host string, port int) (net.Conn, error)
	// IsLocalPeer reports whether host is reachable over the local network
	// (directly peered by LAN discovery, or a loopback/private address).
	IsLocalPeer(host string) bool
	Close() error
}

// isLocalIP reports whether ip belongs to a loopback, link-local or private network.
func isLocalIP(ip net.IP) bool {
	return ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate())
}

// uriHostIP extracts the IP of a peer URI such as tls://[fe80::1%en0]:1234.
func uriHostIP(uri string) net.IP {
	u, err := url.Parse(uri)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // zone of a link-local address
	}
	return net.ParseIP(host)
}

func SetupTransport(kind TransportKind, verbose bool, cfgPath string) (Transport, error) {
	switch kind {
	case "", TransportYggdrasil:
		return newYggTransport(verbose, cfgPath)
	case TransportPlain:
		return newPlainTransport(verbose, cfgPath)
	default:
		return nil, fmt.Errorf("unsupported transport %q", kind)
	}
}
