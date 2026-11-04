package pwa_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/openshelf/internal/testutil"
	"github.com/casmith/openshelf/internal/web/static"
)

func TestManifestAndIcons(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.Client(t)
	res := c.Get("/manifest.webmanifest")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var m struct {
		Name, StartURL, Scope, Display string
		Icons                          []struct{ Src, Type, Purpose string }
	}
	if err := json.Unmarshal([]byte(res.Body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "OpenShelf" || m.Display != "standalone" || len(m.Icons) < 3 {
		t.Fatalf("manifest: %+v", m)
	}
	maskable := false
	for _, ic := range m.Icons {
		r := c.Get(ic.Src)
		if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != ic.Type {
			t.Errorf("%s: %d %q", ic.Src, r.StatusCode, r.Header.Get("Content-Type"))
		}
		if !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: cache-control %q", ic.Src, r.Header.Get("Cache-Control"))
		}
		maskable = maskable || ic.Purpose == "maskable"
	}
	if !maskable {
		t.Error("no maskable icon")
	}
}

func TestServiceWorker(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.Client(t)
	res := c.Get("/sw.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sw.js: %d", res.StatusCode)
	}
	if got := res.Header.Get("Service-Worker-Allowed"); got != "/" {
		t.Errorf("Service-Worker-Allowed = %q", got)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") {
		t.Errorf("content-type %q", res.Header.Get("Content-Type"))
	}
	if !strings.Contains(res.Header.Get("Cache-Control"), "no-cache") {
		t.Errorf("cache-control %q", res.Header.Get("Cache-Control"))
	}
	for _, want := range []string{`"/offline"`, static.URL("css/app.css")} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("sw.js does not precache %s", want)
		}
	}
	if strings.Contains(res.Body, "__VERSION__") || strings.Contains(res.Body, `"/"`) {
		t.Error("sw.js must not precache pages or contain placeholders")
	}
}

func TestOfflinePageAndRegisterScript(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.Client(t)
	res := c.Get("/offline")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "You're offline") {
		t.Fatalf("offline: %d", res.StatusCode)
	}
	res = c.Get("/pwa/register.js")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, `register("/sw.js"`) {
		t.Fatalf("register.js: %d", res.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, s.URL+"/pwa/register.js", nil)
	req.Header.Set("If-None-Match", res.Header.Get("ETag"))
	if r := c.Do(req); r.StatusCode != http.StatusNotModified {
		t.Errorf("conditional register.js: %d", r.StatusCode)
	}
	if r := c.Get("/pwa/nope.png"); r.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset: %d", r.StatusCode)
	}
}
