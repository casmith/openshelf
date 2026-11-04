package safehttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestDefaultBlocksLoopbackAndMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	c := New(Options{})
	for _, u := range []string{
		srv.URL,
		strings.Replace(srv.URL, "127.0.0.1", "localhost", 1),
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://10.0.0.1/",
		"http://0.0.0.0/",
	} {
		if _, _, err := c.Get(context.Background(), u); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err = %v, want ErrBlocked", u, err)
		}
	}
}

func TestAllowed(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":            true,
		"2606:4700::1111":    true,
		"127.0.0.1":          false,
		"::1":                false,
		"169.254.169.254":    false,
		"10.1.2.3":           false,
		"172.16.0.1":         false,
		"192.168.1.1":        false,
		"100.64.0.1":         false,
		"100.100.100.200":    false,
		"224.0.0.1":          false,
		"0.0.0.0":            false,
		"::":                 false,
		"fe80::1":            false,
		"fd00:ec2::254":      false,
		"::ffff:127.0.0.1":   false,
		"::ffff:169.254.1.1": false,
		"255.255.255.255":    false,
	}
	for s, want := range cases {
		if got := Allowed(netip.MustParseAddr(s), false); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", s, got, want)
		}
	}
	if !Allowed(netip.MustParseAddr("127.0.0.1"), true) {
		t.Error("AllowLoopback should permit 127.0.0.1")
	}
	if Allowed(netip.MustParseAddr("169.254.169.254"), true) {
		t.Error("AllowLoopback must not permit metadata")
	}
}

func TestSchemes(t *testing.T) {
	c := New(Options{AllowLoopback: true})
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "gopher://x/", "http:///nohost"} {
		if _, _, err := c.Get(context.Background(), u); err == nil {
			t.Errorf("%s: expected error", u)
		}
	}
}

func TestRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/r/{n}", func(w http.ResponseWriter, r *http.Request) {
		n := r.PathValue("n")
		if n == "0" {
			_, _ = w.Write([]byte("done"))
			return
		}
		http.Redirect(w, r, "/r/"+string(rune(n[0]-1)), http.StatusFound)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	})
	mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(Options{AllowLoopback: true})
	ctx := context.Background()

	b, _, err := c.Get(ctx, srv.URL+"/r/3")
	if err != nil || string(b) != "done" {
		t.Fatalf("3 redirects: %q %v", b, err)
	}
	if _, _, err := c.Get(ctx, srv.URL+"/r/4"); err == nil {
		t.Error("4 redirects should fail")
	}
	if _, _, err := c.Get(ctx, srv.URL+"/file"); !errors.Is(err, ErrScheme) {
		t.Errorf("redirect to file: %v", err)
	}
	if _, _, err := c.Get(ctx, srv.URL+"/meta"); !errors.Is(err, ErrBlocked) {
		t.Errorf("redirect to metadata: %v", err)
	}
}

func TestBodyLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/len", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(make([]byte, 101)) })
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		for range 3 {
			_, _ = w.Write(make([]byte, 50))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, 100))
	})
	mux.HandleFunc("/404", http.NotFound)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(Options{AllowLoopback: true, MaxBytes: 100})
	ctx := context.Background()
	for _, p := range []string{"/len", "/chunked"} {
		if _, _, err := c.Get(ctx, srv.URL+p); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v", p, err)
		}
	}
	b, ct, err := c.Get(ctx, srv.URL+"/ok")
	if err != nil || len(b) != 100 || ct != "image/png" {
		t.Errorf("ok: %d %q %v", len(b), ct, err)
	}
	if _, _, err := c.Get(ctx, srv.URL+"/404"); err == nil {
		t.Error("404 should fail")
	}
}
