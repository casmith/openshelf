package dashboard

import (
	"net/http"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
)

// Register adds this package's routes. Owned by task S06 (docs/plan.md).
func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.Handle("GET /{$}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		web.Render(w, r, http.StatusOK, home(web.PageFor(r, d, "Home", "home")))
	})))
}
