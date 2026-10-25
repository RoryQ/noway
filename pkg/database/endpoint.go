package database

import (
	"net"
	"strings"
)

// IsLoopbackHost checks whether an endpoint host is a loopback address (e.g. localhost, 127.0.0.1, [::1]).
func IsLoopbackHost(endpoint string) bool {
	u := strings.TrimSpace(endpoint)
	if strings.HasPrefix(u, "http://") {
		u = strings.TrimPrefix(u, "http://")
	} else if strings.HasPrefix(u, "https://") {
		u = strings.TrimPrefix(u, "https://")
	}
	if idx := strings.IndexAny(u, "/?#"); idx != -1 {
		u = u[:idx]
	}
	host := u
	if h, _, err := net.SplitHostPort(u); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}
