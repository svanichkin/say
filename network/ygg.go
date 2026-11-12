package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ygg "github.com/svanichkin/ygg"
	"github.com/yggdrasil-network/yggdrasil-go/src/address"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type yggTransport struct {
	node      *ygg.Node
	addr      string
	readyCh   chan struct{}
	readyOnce sync.Once
	statsOnce sync.Once
	statsStop chan struct{}
}

// lanEnabled turns on Yggdrasil LAN discovery (multicast); see SetLAN.
var lanEnabled = true

// lanPassword is the LAN discovery password of Say nodes: they peer over the
// local network only with each other.
const lanPassword = "say-lan-v1"

// SetLAN enables or disables LAN discovery for transports set up afterwards.
func SetLAN(enabled bool) {
	lanEnabled = enabled
}

// directEnabled turns on direct links (see SetDirect).
var directEnabled = true

// SetDirect enables or disables direct links for transports set up
// afterwards: a call to a peer that has them on too goes straight between
// the two machines, through their NATs, instead of through public Yggdrasil
// peers. Each side then sees the other's public IP address.
func SetDirect(enabled bool) {
	directEnabled = enabled
}

func newYggTransport(verbose bool, cfgPath string) (Transport, error) {
	t := &yggTransport{
		readyCh:   make(chan struct{}),
		statsStop: make(chan struct{}),
	}

	log.Printf("[net] loading")
	ygg.SetVerbose(verbose)
	// Three peers, the fastest: enough for the route to go around a loaded
	// or distant relay (with a single peer every call went through it, and
	// one bad relay made a bad call), few enough that the node is not a
	// popular transit router (with dozens of fast peers it was). The library
	// keeps the ranked backups and replaces lost peers within seconds.
	ygg.SetMaxPeers(3)
	// LAN discovery: peers in the same local network (or on the same machine)
	// find each other by multicast and connect directly instead of through
	// public peers on the internet, which cuts latency and jitter. The
	// password limits it to Say nodes: another Yggdrasil node nearby (the
	// system's own, say) would otherwise peer too and route through us.
	ygg.SetLAN(lanEnabled, lanPassword)
	ygg.SetDirect(directEnabled)
	ygg.SetConnectivityHandler(func(connected bool) {
		if yggConnected.Swap(connected) != connected { // DEBUG-TEMP
			log.Printf("[debug] ygg: connected to the network: %v", connected)
		}
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
	pc, err := ygg.ListenUDP(port)
	if err == nil && t.node != nil && t.node.Net != nil {
		// The netstack exists once a socket is open.
		st := t.node.Net.Stack
		t.statsOnce.Do(func() {
			go logNetstackDrops(st, t.statsStop)
			go watchPeers(t.node.Core, t.statsStop) // DEBUG-TEMP
		})
	}
	return pc, err
}

// logNetstackDrops logs packets the in-process netstack itself dropped, so
// local overload can be told apart from loss on the Yggdrasil path: drops
// as they happen, a summary every 10 s.
func logNetstackDrops(st *stack.Stack, stop <-chan struct{}) {
	if st == nil {
		return
	}
	type counts struct{ rx, rxFull, tx, txErr, nicFull, ipErr uint64 }
	read := func() counts {
		s := st.Stats()
		return counts{
			rx:      s.UDP.PacketsReceived.Value(),
			rxFull:  s.UDP.ReceiveBufferErrors.Value(),
			tx:      s.UDP.PacketsSent.Value(),
			txErr:   s.UDP.PacketSendErrors.Value(),
			nicFull: s.NICs.TxPacketsDroppedNoBufferSpace.Value(),
			ipErr:   s.IP.OutgoingPacketErrors.Value(),
		}
	}
	prev, sum := read(), read()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for n := 1; ; n++ {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		c := read()
		if c.rxFull != prev.rxFull || c.txErr != prev.txErr || c.nicFull != prev.nicFull || c.ipErr != prev.ipErr {
			log.Printf("[net/stack] drops in 1 s: rx buffer full %d, udp send errors %d, tx queue full %d, ip send errors %d (rx %d, tx %d pkts)",
				c.rxFull-prev.rxFull, c.txErr-prev.txErr, c.nicFull-prev.nicFull, c.ipErr-prev.ipErr, c.rx-prev.rx, c.tx-prev.tx)
		}
		prev = c
		if n%10 == 0 {
			log.Printf("[net/stack] 10 s: rx %d pkts (%d dropped, buffer full), tx %d pkts (%d udp errors, %d tx queue full, %d ip errors)",
				c.rx-sum.rx, c.rxFull-sum.rxFull, c.tx-sum.tx, c.txErr-sum.txErr, c.nicFull-sum.nicFull, c.ipErr-sum.ipErr)
			sum = c
		}
	}
}

func (t *yggTransport) DialTCP(host string, port int) (net.Conn, error) {
	return ygg.DialTCP(host, port)
}

func (t *yggTransport) StartDirect(host string) func() {
	return ygg.StartDirect(host)
}

func (t *yggTransport) Close() error {
	if t == nil || t.node == nil {
		return nil
	}
	select {
	case <-t.statsStop:
	default:
		close(t.statsStop)
	}
	return t.node.Close()
}

// IsLocalPeer reports whether host is a direct Yggdrasil peer connected over
// a local address, which is how LAN discovery (multicast) peers appear.
func (t *yggTransport) IsLocalPeer(host string) bool {
	return t.peeredWith(host, func(uri string) bool { return isLocalIP(uriHostIP(uri)) })
}

// IsDirectPeer reports whether host is one of the node's own Yggdrasil
// peers (a direct link through the NATs, or LAN discovery): the traffic
// then goes straight to it, through no other node.
func (t *yggTransport) IsDirectPeer(host string) bool {
	return t.peeredWith(host, func(string) bool { return true })
}

// peeredWith reports whether host is an up Yggdrasil peer whose link URI
// passes ok.
func (t *yggTransport) peeredWith(host string, ok func(uri string) bool) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || t.node == nil || t.node.Core == nil {
		return false
	}
	for _, p := range t.node.Core.GetPeers() {
		if !p.Up || len(p.Key) == 0 {
			continue
		}
		if net.IP(address.AddrForKey(p.Key)[:]).Equal(ip) && ok(p.URI) {
			return true
		}
	}
	return false
}

// yggConnected is whether the node has peers (DEBUG-TEMP).
var yggConnected atomic.Bool

// Connected reports whether the Yggdrasil node has peers (DEBUG-TEMP: for
// the call health log).
func Connected() bool { return yggConnected.Load() }
