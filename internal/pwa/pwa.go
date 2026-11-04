package pwa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web/static"
)

//go:embed assets
var assetFS embed.FS

//go:embed sw.js
var swTemplate string

var assetHashes = func() map[string]string {
	m := map[string]string{}
	entries, _ := fs.ReadDir(assetFS, "assets")
	for _, e := range entries {
		b, _ := assetFS.ReadFile("assets/" + e.Name())
		m[e.Name()] = shortHash(b)
	}
	return m
}()

const (
	ThemeLight = "#f6f5fb"
	ThemeDark  = "#12101f"
)

// AssetURL returns a cache-busted URL for a file in internal/pwa/assets.
func AssetURL(name string) string {
	return "/pwa/" + name + "?v=" + assetHashes[name]
}

func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.HandleFunc("GET /manifest.webmanifest", manifest)
	mux.HandleFunc("GET /sw.js", serviceWorker)
	mux.HandleFunc("GET /offline", offline)
	mux.HandleFunc("GET /pwa/{file}", asset)
}

type icon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

var manifestJSON = sync.OnceValue(func() []byte {
	b, _ := json.Marshal(map[string]any{
		"id":               "/",
		"name":             "OpenShelf",
		"short_name":       "OpenShelf",
		"description":      "Track your games, books, films, and shows.",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": ThemeDark,
		"theme_color":      ThemeDark,
		"icons": []icon{
			{AssetURL("icon.svg"), "any", "image/svg+xml", "any"},
			{AssetURL("icon-192.png"), "192x192", "image/png", "any"},
			{AssetURL("icon-512.png"), "512x512", "image/png", "any"},
			{AssetURL("icon-maskable-512.png"), "512x512", "image/png", "maskable"},
		},
	})
	return b
})

func manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(manifestJSON())
}

func precache() []string {
	return []string{
		"/offline",
		static.URL("css/app.css"),
		static.URL("htmx.min.js"),
		static.URL("logo.svg"),
		AssetURL("register.js"),
		AssetURL("icon.svg"),
		AssetURL("icon-192.png"),
	}
}

var swJS = sync.OnceValue(func() []byte {
	list, _ := json.Marshal(precache())
	version := shortHash(append(append([]byte(swTemplate), list...), offlineHTML()...))
	js := strings.NewReplacer("__VERSION__", version, "__PRECACHE__", string(list)).Replace(swTemplate)
	return []byte(js)
})

func serviceWorker(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Service-Worker-Allowed", "/")
	h.Set("Cache-Control", "no-cache, max-age=0")
	_, _ = w.Write(swJS())
}

var offlineHTML = sync.OnceValue(func() []byte {
	var buf bytes.Buffer
	_ = offlinePage().Render(context.Background(), &buf)
	return buf.Bytes()
})

func offline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(offlineHTML())
}

func asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	sum, ok := assetHashes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, _ := assetFS.ReadFile(path.Join("assets", name))
	if r.URL.Query().Get("v") == sum {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("ETag", `"`+sum+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

func shortHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:12]
}
