package network

import (
	"fmt"
	"strings"
)

var DefaultListenPort = 7777

// PrettyAddr formats an IPv4/IPv6 host and port string safely, adding brackets
// around IPv6 addresses when required so that host:port parsing remains valid.
func PrettyAddr(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		if port != DefaultListenPort {
			return fmt.Sprintf("[%s]:%d", host, port)
		} else {
			return fmt.Sprintf("[%s]", host)
		}
	}
	return fmt.Sprintf("%s:%d", host, port)
}
