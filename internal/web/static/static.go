package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed *.js *.svg all:css
var files embed.FS

var hashes = func() map[string]string {
	m := map[string]string{}
	_ = fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		m[path] = hex.EncodeToString(sum[:])[:12]
		return nil
	})
	return m
}()

// URL returns a cache-busted URL for a static asset.
func URL(name string) string {
	return "/static/" + name + "?v=" + hashes[name]
}

func Handler() http.Handler {
	fsrv := http.StripPrefix("/static/", http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		fsrv.ServeHTTP(w, r)
	})
}
