package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

type plainTransport struct {
	addr string
}

func newPlainTransport(verbose bool, cfgPath string) (Transport, error) {
	log.Printf("[net] loading")
	return &plainTransport{addr: detectPlainLocalAddr()}, nil
}

func (t *plainTransport) Kind() TransportKind {
	return TransportPlain
}

func (t *plainTransport) LocalAddr() string {
	return t.addr
}

func (t *plainTransport) WaitReady(ctx context.Context, timeout time.Duration) error {
	return nil
}

func (t *plainTransport) ListenTCP(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
}

func (t *plainTransport) ListenUDP(port int) (net.PacketConn, error) {
	return net.ListenPacket("udp", fmt.Sprintf(":%d", port))
}

func (t *plainTransport) DialTCP(host string, port int) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 15 * time.Second,
	}
	return dialer.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

func (t *plainTransport) Close() error {
	return nil
}

func detectPlainLocalAddr() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	var fallbackV6 string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet == nil {
			continue
		}
		ip := ipNet.IP
		if ip == nil || ip.IsLoopback() || !ip.IsGlobalUnicast() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		if fallbackV6 == "" {
			fallbackV6 = ip.String()
		}
	}

	return fallbackV6
}

func (t *plainTransport) IsLocalPeer(host string) bool {
	return isLocalIP(net.ParseIP(strings.Trim(host, "[]")))
}
