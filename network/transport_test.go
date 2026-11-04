package network

import "testing"

func TestLocalPeerDetection(t *testing.T) {
	for uri, want := range map[string]bool{
		"tls://[fe80::1c2b:3aff:fe4d:5e6f%25en0]:34567?key=ab": true, // LAN discovery, as url.URL.String writes it
		"tls://[fe80::1c2b:3aff:fe4d:5e6f%en0]:34567":          true, // unescaped zone
		"tls://192.168.1.20:443":                               true,
		"tcp://127.0.0.1:9000":                                 true,
		"tls://203.0.113.7:443":                                false, // a public peer
		"quic://[2001:db8::1]:443":                             false,
	} {
		if got := isLocalIP(uriHostIP(uri)); got != want {
			t.Errorf("%s: local=%v, want %v", uri, got, want)
		}
	}
	p := &plainTransport{}
	if !p.IsLocalPeer("127.0.0.1") || !p.IsLocalPeer("[::1]") || p.IsLocalPeer("8.8.8.8") {
		t.Fatal("plain transport: wrong local peer classification")
	}
}
