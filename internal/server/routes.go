package server

import (
	"net/http"

	"github.com/casmith/openshelf/internal/accounts"
	"github.com/casmith/openshelf/internal/achievements"
	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/connections"
	"github.com/casmith/openshelf/internal/dashboard"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/exchange"
	"github.com/casmith/openshelf/internal/experiences"
	"github.com/casmith/openshelf/internal/images"
	"github.com/casmith/openshelf/internal/itempage"
	"github.com/casmith/openshelf/internal/journal"
	"github.com/casmith/openshelf/internal/library"
	"github.com/casmith/openshelf/internal/lists"
	"github.com/casmith/openshelf/internal/login"
	"github.com/casmith/openshelf/internal/profile"
	"github.com/casmith/openshelf/internal/pwa"
	"github.com/casmith/openshelf/internal/reviews"
	"github.com/casmith/openshelf/internal/stats"
	"github.com/casmith/openshelf/internal/suggestions"
	"github.com/casmith/openshelf/internal/sync/steam"
	"github.com/casmith/openshelf/internal/synchistory"
	"github.com/casmith/openshelf/internal/web"
)

func routes(mux *http.ServeMux, d *deps.Deps) {
	login.Register(mux, d)
	itempage.Register(mux, d)
	catalog.Register(mux, d)
	library.Register(mux, d)
	experiences.Register(mux, d)
	reviews.Register(mux, d)
	lists.Register(mux, d)
	dashboard.Register(mux, d)
	journal.Register(mux, d)
	stats.Register(mux, d)
	accounts.Register(mux, d)
	exchange.Register(mux, d)
	discover.Register(mux, d)
	images.Register(mux, d)
	pwa.Register(mux, d)
	connections.Register(mux, d)
	steam.Register(mux, d)
	achievements.Register(mux, d)
	synchistory.Register(mux, d)
	suggestions.Register(mux, d)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if name, ok := profile.Username(r.URL.Path); ok && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			profile.Show(w, r, d, name)
			return
		}
		web.NotFound(w, r, d)
	})
}
