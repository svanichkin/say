package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	ygg "github.com/svanichkin/ygg"
	"github.com/yggdrasil-network/yggdrasil-go/src/address"
)

type yggTransport struct {
	node      *ygg.Node
	addr      string
	readyCh   chan struct{}
	readyOnce sync.Once
}

// lanEnabled turns on Yggdrasil LAN discovery (multicast); see SetLAN.
var lanEnabled = true

// SetLAN enables or disables LAN discovery for transports set up afterwards.
func SetLAN(enabled bool) {
	lanEnabled = enabled
}

func newYggTransport(verbose bool, cfgPath string) (Transport, error) {
	t := &yggTransport{
		readyCh: make(chan struct{}),
	}

	log.Printf("[net] loading")
	ygg.SetVerbose(verbose)
	ygg.SetMaxPeers(100)
	// LAN discovery: peers in the same local network (or on the same machine)
	// find each other by multicast and connect directly instead of through
	// public peers on the internet, which cuts latency and jitter.
	ygg.SetLAN(lanEnabled, "")
	ygg.SetConnectivityHandler(func(connected bool) {
		if connected {
			t.readyOnce.Do(func() {
				close(t.readyCh)
			})
		}
	})

	node, err := ygg.New(cfgPath)
	if err != nil {
		return nil, err
	}

	t.node = node
	t.addr = node.Core.Address().String()
	return t, nil
}

func (t *yggTransport) Kind() TransportKind {
	return TransportYggdrasil
}

func (t *yggTransport) LocalAddr() string {
	return t.addr
}

func (t *yggTransport) WaitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.readyCh:
		return nil
	case <-timer.C:
		return fmt.Errorf("ygg connectivity timeout after %s", timeout)
	}
}

func (t *yggTransport) ListenTCP(port int) (net.Listener, error) {
	return ygg.ListenTCP(port)
}

func (t *yggTransport) ListenUDP(port int) (net.PacketConn, error) {
	return ygg.ListenUDP(port)
}

func (t *yggTransport) DialTCP(host string, port int) (net.Conn, error) {
	return ygg.DialTCP(host, port)
}

func (t *yggTransport) Close() error {
	if t == nil || t.node == nil {
		return nil
	}
	return t.node.Close()
}

// IsLocalPeer reports whether host is a direct Yggdrasil peer connected over
// a local address, which is how LAN discovery (multicast) peers appear.
func (t *yggTransport) IsLocalPeer(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || t.node == nil || t.node.Core == nil {
		return false
	}
	for _, p := range t.node.Core.GetPeers() {
		if !p.Up || len(p.Key) == 0 {
			continue
		}
		if net.IP(address.AddrForKey(p.Key)[:]).Equal(ip) && isLocalIP(uriHostIP(p.URI)) {
			return true
		}
	}
	return false
}
