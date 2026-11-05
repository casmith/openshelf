package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the request's client address. X-Forwarded-For is honored only
// when the direct peer is trusted, taking the right-most untrusted hop.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isTrusted(host, trusted) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" {
			continue
		}
		if _, err := netip.ParseAddr(hop); err != nil {
			return host
		}
		if !isTrusted(hop, trusted) {
			return hop
		}
	}
	return host
}

func isTrusted(ip string, trusted []netip.Prefix) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
