package web

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:1234", "", "203.0.113.9"},
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},
		{"10.42.3.7:5555", "198.51.100.2", "198.51.100.2"},
		{"10.42.3.7:5555", "1.1.1.1, 198.51.100.2, 10.42.0.9", "198.51.100.2"},
		{"10.42.3.7:5555", "", "10.42.3.7"},
		{"10.42.3.7:5555", "garbage", "10.42.3.7"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r, trusted); got != c.want {
			t.Errorf("remote=%s xff=%q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
