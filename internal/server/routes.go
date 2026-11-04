package server

import (
	"net/http"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/login"
	"github.com/casmith/openshelf/internal/web"
)

func routes(mux *http.ServeMux, d *deps.Deps) {
	login.Register(mux, d)

	mux.Handle("GET /{$}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		web.Render(w, r, http.StatusOK, home(web.PageFor(r, d, "Home", "home")))
	})))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.NotFound(w, r, d)
	})
}
