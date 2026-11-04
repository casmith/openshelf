package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/a-h/templ"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/web/ui"
)

const flashKey = "flash"

// PageFor builds layout data for the current request and consumes any pending flash.
func PageFor(r *http.Request, d *deps.Deps, title, nav string) ui.Page {
	p := ui.Page{Title: title, Nav: nav}
	if u, ok := auth.CurrentUser(r.Context()); ok {
		p.User = &u
	}
	p.Flash = d.Sessions.PopString(r.Context(), flashKey)
	return p
}

func Flash(ctx context.Context, d *deps.Deps, msg string) {
	d.Sessions.Put(ctx, flashKey, msg)
}

func IsHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// IsBoosted reports an hx-boost navigation, which expects a full page.
func IsBoosted(r *http.Request) bool { return r.Header.Get("HX-Boosted") == "true" }

// IsPartial reports a non-boosted htmx request, which expects only a fragment.
func IsPartial(r *http.Request) bool { return IsHTMX(r) && !IsBoosted(r) }

func Render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// Redirect sends a 303, or HX-Redirect for partial htmx requests.
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if IsPartial(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func NotFound(w http.ResponseWriter, r *http.Request, d *deps.Deps) {
	Render(w, r, http.StatusNotFound, errorPage(PageFor(r, d, "Not found", ""), "Not found", "That page doesn't exist or you don't have access to it."))
}

// Error renders 404 for pgx.ErrNoRows and 500 (logged) otherwise.
func Error(w http.ResponseWriter, r *http.Request, d *deps.Deps, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		NotFound(w, r, d)
		return
	}
	d.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	Render(w, r, http.StatusInternalServerError, errorPage(PageFor(r, d, "Error", ""), "Something went wrong", "The error has been logged."))
}
