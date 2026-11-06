package suggestions

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web"
	"github.com/casmith/openshelf/internal/web/form"
)

const pagePath = "/suggestions/finished"

type pageView struct {
	HasAccounts bool
	Groups
	Loc *time.Location
}

func Register(mux *http.ServeMux, d *deps.Deps) {
	h := func(fn func(http.ResponseWriter, *http.Request, *deps.Deps, db.User)) http.Handler {
		return auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := auth.CurrentUser(r.Context())
			fn(w, r, d, u)
		}))
	}
	mux.Handle("GET "+pagePath, h(index))
	mux.Handle("GET "+pagePath+"/all", h(confirmAll))
	mux.Handle("POST "+pagePath+"/all", h(markAll))
	mux.Handle("POST "+pagePath+"/{id}/finish", h(act(actFinish)))
	mux.Handle("POST "+pagePath+"/{id}/finish-undated", h(act(actUndated)))
	mux.Handle("POST "+pagePath+"/{id}/dismiss", h(act(actDismiss)))
}

func index(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	has, err := d.Q.SuggestionsHasAccounts(r.Context(), viewer.ID)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	v := pageView{HasAccounts: has, Loc: location(viewer)}
	if has {
		if v.Groups, err = List(r.Context(), d.Q, viewer); err != nil {
			web.Error(w, r, d, err)
			return
		}
	}
	web.Render(w, r, http.StatusOK, page(web.PageFor(r, d, "Possibly finished", "library"), v))
}

func confirmAll(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	g, err := List(r.Context(), d.Q, viewer)
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Render(w, r, http.StatusOK, confirmPage(web.PageFor(r, d, "Mark all as finished", "library"), g.Strong, location(viewer)))
}

func markAll(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
	n := 0
	err := d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
		g, err := List(r.Context(), q, viewer)
		if err != nil {
			return err
		}
		for _, s := range g.Strong {
			if err := MarkFinished(r.Context(), q, d.Now(), viewer, s, false); err != nil {
				return err
			}
		}
		n = len(g.Strong)
		return nil
	})
	if err != nil {
		web.Error(w, r, d, err)
		return
	}
	web.Flash(r.Context(), d, "Marked "+plural(n, "game")+" as finished.")
	web.Redirect(w, r, pagePath)
}

type action int

const (
	actFinish action = iota
	actUndated
	actDismiss
)

func act(a action) func(http.ResponseWriter, *http.Request, *deps.Deps, db.User) {
	return func(w http.ResponseWriter, r *http.Request, d *deps.Deps, viewer db.User) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			web.NotFound(w, r, d)
			return
		}
		f, err := form.Parse(w, r)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		item, err := d.Q.GetMediaItemByPublicID(r.Context(), id)
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		err = d.WithTx(r.Context(), func(q *db.Queries, _ pgx.Tx) error {
			s, err := For(r.Context(), q, viewer, item.ID)
			if err != nil {
				return err
			}
			if a == actDismiss {
				return q.SuggestionsDismiss(r.Context(), db.SuggestionsDismissParams{UserID: viewer.ID, MediaItemID: item.ID})
			}
			return MarkFinished(r.Context(), q, d.Now(), viewer, s, a == actUndated)
		})
		if err != nil {
			web.Error(w, r, d, err)
			return
		}
		if web.IsPartial(r) {
			web.Render(w, r, http.StatusOK, templ.NopComponent)
			return
		}
		next := pagePath
		if n := f.Get("next"); strings.HasPrefix(n, "/items/") {
			next = n
		}
		if a == actDismiss {
			web.Flash(r.Context(), d, "Okay, "+item.Title+" won't be suggested again.")
		} else {
			web.Flash(r.Context(), d, "Marked "+item.Title+" as finished.")
		}
		web.Redirect(w, r, next)
	}
}

// ItemPanel offers to mark the item finished when it is a current suggestion.
func ItemPanel(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem) (templ.Component, error) {
	if item.Kind != db.MediaKindGame {
		return templ.NopComponent, nil
	}
	s, err := For(ctx, d.Q, viewer, item.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return templ.NopComponent, nil
	}
	if err != nil {
		return nil, err
	}
	return itemPanel(item, s), nil
}

func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}
