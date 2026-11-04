package server_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/testutil"
)

func TestHealth(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.Client(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		if res := c.Get(p); res.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", p, res.StatusCode)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "alice")
	c := s.Client(t)

	res := c.Get("/")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous home: want redirect to login, got %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	res = c.PostForm("/login", url.Values{"username": {"alice"}, "password": {"nope"}})
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "Incorrect username or password") {
		t.Fatalf("bad password: status %d", res.StatusCode)
	}

	res = c.PostForm("/login", url.Values{"username": {"alice"}, "password": {testutil.TestPassword}, "next": {"/"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status %d", res.StatusCode)
	}
	res = c.Get("/")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, u.DisplayName) {
		t.Fatalf("home after login: status %d", res.StatusCode)
	}

	if res := c.PostForm("/logout", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: status %d", res.StatusCode)
	}
	if res := c.Get("/"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("home after logout: want redirect, got %d", res.StatusCode)
	}
}

func TestDisabledUserIsLoggedOut(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "bob")
	c := s.LoginAs(t, u)
	if _, err := s.Pool.Exec(context.Background(), "update users set disabled_at = $1 where id = $2", time.Now(), u.ID); err != nil {
		t.Fatal(err)
	}
	if res := c.Get("/"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("disabled user: want redirect, got %d", res.StatusCode)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	s := testutil.NewServer(t)
	u := testutil.CreateUser(t, s.Q, "carol")
	c := s.LoginAs(t, u)
	req, _ := http.NewRequest(http.MethodPost, s.URL+"/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if res := c.Do(req); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST: want 403, got %d", res.StatusCode)
	}
}

func TestSecurityHeadersAndStatic(t *testing.T) {
	s := testutil.NewServer(t)
	res := s.Client(t).Get("/login")
	if res.Header.Get("Content-Security-Policy") == "" || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing security headers")
	}
	res = s.Client(t).Get("/static/htmx.min.js?v=x")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("static: status %d cache %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
}

func TestLoginRedirectsToSetupWhenEmpty(t *testing.T) {
	s := testutil.NewServer(t)
	res := s.Client(t).Get("/login")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/setup" {
		t.Fatalf("want redirect to /setup, got %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}
