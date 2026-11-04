package dashboard

import (
	"net/http"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
)

func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.Handle("GET /{$}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viewer, _ := auth.CurrentUser(r.Context())
		h, err := load(r.Context(), d, viewer)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		web.Render(w, r, http.StatusOK, home(web.PageFor(r, d, "Home", "home"), h))
	})))
}
