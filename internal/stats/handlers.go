package stats

import (
	"net/http"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
)

// Register adds this package's routes.
func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.Handle("GET /stats", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viewer, _ := auth.CurrentUser(r.Context())
		p := ParsePeriod(r.URL.Query(), d.Now(), viewer.Timezone)
		s, err := Load(r.Context(), d.Q, viewer, p)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		if web.IsPartial(r) {
			web.Render(w, r, http.StatusOK, content(s))
			return
		}
		web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, "Statistics", "stats"), s))
	})))
}
