package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	ygg "github.com/svanichkin/ygg"
)

type yggTransport struct {
	node      *ygg.Node
	addr      string
	readyCh   chan struct{}
	readyOnce sync.Once
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
	ygg.SetLAN(true, "")
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
