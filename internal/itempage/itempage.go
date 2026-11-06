// Package itempage composes the item detail page from each feature's panel.
// Feature tasks fill in their own ItemPanel; they don't edit this package.
package itempage

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/achievements"
	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/experiences"
	"github.com/casmith/openshelf/internal/library"
	"github.com/casmith/openshelf/internal/lists"
	"github.com/casmith/openshelf/internal/reviews"
	"github.com/casmith/openshelf/internal/suggestions"
	"github.com/casmith/openshelf/internal/synchistory"
	"github.com/casmith/openshelf/internal/web"
)

type panelFunc func(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error)

var panels = []panelFunc{
	library.ItemPanel,
	experiences.ItemPanel,
	suggestions.ItemPanel,
	achievements.ItemPanel,
	synchistory.ItemPanel,
	reviews.ItemPanel,
	lists.ItemPanel,
}

func Register(mux *http.ServeMux, d *deps.Deps) {
	mux.Handle("GET /items/{id}", auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		show(w, r, d)
	})))
}

func show(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		web.NotFound(w, r, d)
		return
	}
	item, err := d.Q.GetMediaItemByPublicID(r.Context(), id)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	viewer, _ := auth.CurrentUser(r.Context())
	header, err := catalog.Header(r.Context(), d, viewer, item)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	var rendered []templ.Component
	for _, p := range panels {
		c, err := p(r.Context(), d, viewer, item)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		rendered = append(rendered, c)
	}
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, item.Title, string(item.Kind)), header, rendered))
}
