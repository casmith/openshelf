package server

import (
	"net/http"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web/static"
)

// New returns the root handler. Static assets and health checks bypass sessions.
func New(d *deps.Deps) http.Handler {
	app := http.NewServeMux()
	routes(app, d)

	csrf := http.NewCrossOriginProtection()
	appHandler := chain(app,
		csrf.Handler,
		d.Sessions.LoadAndSave,
		auth.LoadUser(d),
	)

	root := http.NewServeMux()
	root.Handle("GET /static/", static.Handler())
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	root.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	root.Handle("/", appHandler)

	return chain(root, recoverer(d.Log), requestID, accessLog(d.Log, d.Config.TrustedProxies), securityHeaders)
}
