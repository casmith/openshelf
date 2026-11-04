// Package safehttp fetches arbitrary user- or provider-supplied URLs without
// reaching internal networks.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

const (
	DefaultMaxBytes = 10 << 20
	DefaultTimeout  = 30 * time.Second
	maxRedirects    = 3
)

var (
	ErrBlocked  = errors.New("safehttp: address not allowed")
	ErrScheme   = errors.New("safehttp: only http and https are allowed")
	ErrTooLarge = errors.New("safehttp: response too large")
)

type Options struct {
	// AllowLoopback permits 127.0.0.0/8 and ::1. Only tests should set it.
	AllowLoopback bool
	MaxBytes      int64
	Timeout       time.Duration
}

type Client struct {
	http     *http.Client
	maxBytes int64
}

var blockedPrefixes = func() []netip.Prefix {
	var ps []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",
		"100.64.0.0/10", // CGNAT; also Alibaba metadata
		"192.0.0.0/24",
		"198.18.0.0/15",
		"240.0.0.0/4",
		"64:ff9b::/96", // NAT64 can map to private IPv4
		"2002::/16",    // 6to4 likewise
	} {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	return ps
}()

// Allowed reports whether ip is a public unicast address.
func Allowed(ip netip.Addr, allowLoopback bool) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return allowLoopback
	}
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func New(o Options) *Client {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		// Control sees the resolved address, so DNS rebinding can't slip past.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !Allowed(ap.Addr(), o.AllowLoopback) {
				return fmt.Errorf("%w: %s", ErrBlocked, address)
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          4,
	}
	return &Client{
		maxBytes: o.MaxBytes,
		http: &http.Client{
			Transport: tr,
			Timeout:   o.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > maxRedirects {
					return errors.New("safehttp: too many redirects")
				}
				return checkScheme(req.URL)
			},
		},
	}
}

func checkScheme(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrScheme
	}
	if u.Hostname() == "" {
		return errors.New("safehttp: missing host")
	}
	return nil
}

// Get fetches rawURL and returns the body (at most MaxBytes) and its Content-Type.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	if err := checkScheme(u); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "OpenShelf")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", fmt.Errorf("safehttp: unexpected status %d", res.StatusCode)
	}
	if res.ContentLength > c.maxBytes {
		return nil, "", ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, c.maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(b)) > c.maxBytes {
		return nil, "", ErrTooLarge
	}
	return b, res.Header.Get("Content-Type"), nil
}
